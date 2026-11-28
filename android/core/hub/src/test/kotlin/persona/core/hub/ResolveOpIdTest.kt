package persona.core.hub

import persona.core.contracts.Interaction
import persona.core.contracts.Resolution
import persona.core.contracts.api.InteractionGoneException
import persona.core.contracts.api.ResolveOutcome
import java.io.IOException
import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.test.runTest
import okhttp3.Protocol
import okhttp3.Request
import okhttp3.Response
import okhttp3.ResponseBody.Companion.toResponseBody
import org.junit.Assert.*
import org.junit.Test

/** An answer cut off by a restarting hub goes again under the same op id, for a bounded time (#1574). */
@OptIn(ExperimentalCoroutinesApi::class)
class ResolveOpIdTest {
    private class Sent(val path: String, val opId: String?, val body: String, val feature: String?)

    private fun ok(path: String) = Response.Builder().request(Request.Builder().url("https://hub.example$path").build())
        .protocol(Protocol.HTTP_1_1).code(200).message("OK").body("{}".toResponseBody()).build()

    @Test fun aCutOffAnswerIsRetriedWithTheSameOpId() = runTest {
        val sent = mutableListOf<Sent>()
        val failures = ArrayDeque(listOf<Exception>(
            HubFailure.from(java.net.ConnectException("refused")),
            HubFailure(HubError.Server, "HTTP 503", status = 503),
            HubFailure.from(IOException("connection reset")),
        ))
        var waits = 0
        var ids = 0
        val transport = HubChatTransport({ path, _, body, headers ->
            val buffer = okio.Buffer().also { body!!.writeTo(it) }
            sent += Sent(path, headers[HubChatTransport.OP_ID_HEADER], buffer.readUtf8(), headers["X-Persona-Features"])
            failures.removeFirstOrNull()?.let { throw it }
            ok(path)
        }, awaitOnline = { waits++; true }, clockMillis = { testScheduler.currentTime }, newOpId = { "op-${++ids}" })

        transport.resolve("ix", Resolution(allow = true))
        assertEquals(4, sent.size)
        assertEquals(setOf("op-1"), sent.map { it.opId }.toSet())
        assertEquals(1, sent.map { it.body }.toSet().size)
        assertEquals("/api/v1/interactions/ix/resolve", (sent.first().path).zhAny())
        assertEquals(3, waits)

        // The next answer is another operation.
        transport.resolve("ix2", Resolution(allow = false))
        assertEquals("op-2", (sent.last().opId).zhAny())
    }

    @Test fun aDisplayedOccurrenceIsPinnedOnTheResolveRequest() = runTest {
        var occurrence: String? = null
        val transport = HubChatTransport({ path, _, _, headers ->
            occurrence = headers["X-Persona-Interaction-Created-At"]
            ok(path)
        }, awaitOnline = { true }, clockMillis = { testScheduler.currentTime })
        val card = Interaction(id = "ix", createdAt = "2026-11-24T01:02:03.123456789Z")
        transport.resolveOccurrence(card, Resolution(allow = true))
        assertEquals(card.createdAt, occurrence)
    }

    @Test fun theHandoffFeatureStaysOnOneFullCardApprovalAndItsRetries() = runTest {
        val sent = mutableListOf<Sent>()
        val failures = ArrayDeque(listOf<Exception>(HubFailure.from(IOException("cut off")), HubFailure.from(IOException("cut off again"))))
        var ids = 0
        val transport = HubChatTransport({ path, _, body, headers ->
            val buffer = okio.Buffer().also { body!!.writeTo(it) }
            sent += Sent(path, headers[HubChatTransport.OP_ID_HEADER], buffer.readUtf8(), headers["X-Persona-Features"])
            failures.removeFirstOrNull()?.let { throw it }
            ok(path)
        }, awaitOnline = { true }, clockMillis = { testScheduler.currentTime }, newOpId = { "op-${++ids}" })

        transport.resolveOutcome("ix", Resolution(allow = true), handoffCard = true)
        assertEquals(3, sent.size)
        assertEquals(setOf("op-1"), sent.map { it.opId }.toSet())
        assertEquals(setOf("secret_handoff"), sent.map { it.feature }.toSet())
        assertEquals(setOf("""{"allow":true}"""), sent.map { it.body }.toSet())

        transport.resolveOutcome("ix", Resolution(allow = true, always = true), handoffCard = true)
        assertEquals("secret_handoff", sent.last().feature)
        assertEquals("""{"allow":true,"always":true}""", sent.last().body)
        transport.resolveOutcome("ix", Resolution(allow = false), handoffCard = true)
        assertNull(sent.last().feature)
        transport.resolveOutcome("ix", Resolution(allow = true), handoffCard = false)
        assertNull(sent.last().feature)
        transport.resolve("ix", Resolution(allow = true))
        assertNull(sent.last().feature)

        transport.resolveOutcome("ix", Resolution(allow = true), handoffCard = true, secretRunEnv = true)
        assertEquals("secret_run_env", sent.last().feature)
        transport.resolveOutcome("ix", Resolution(allow = true), handoffCard = false, secretRunEnv = true)
        assertNull(sent.last().feature)
        transport.resolveOutcome("ix", Resolution(allow = false), handoffCard = true, secretRunEnv = true)
        assertNull(sent.last().feature)
    }

    @Test fun retriesStopAfterTheBoundAndOtherFailuresAreNotRetried() = runTest {
        var attempts = 0
        val bounded = HubChatTransport({ _, _, _, _ -> attempts++; throw HubFailure.from(IOException("reset")) },
            awaitOnline = { true }, clockMillis = { testScheduler.currentTime })
        val error = runCatching { bounded.resolve("ix", Resolution(allow = true)) }.exceptionOrNull()
        assertEquals(HubError.Transport, (error as HubFailure).kind)
        assertTrue(testScheduler.currentTime in HubChatTransport.RESOLVE_RETRY_MILLIS..(HubChatTransport.RESOLVE_RETRY_MILLIS + 1_000))
        assertTrue(attempts in 100..122)

        // The connection never came back within the bound: the answer is left to be checked.
        attempts = 0
        val offline = HubChatTransport({ _, _, _, _ -> attempts++; throw HubFailure.from(java.net.ConnectException("refused")) },
            awaitOnline = { false }, clockMillis = { testScheduler.currentTime })
        assertTrue(runCatching { offline.resolve("ix", Resolution(allow = true)) }.exceptionOrNull() is HubFailure)
        assertEquals(1, attempts)

        // A refusal from the hub itself is final; a gone interaction stays what it was.
        attempts = 0
        val refused = HubChatTransport({ _, _, _, _ -> attempts++; throw HubFailure(HubError.Server, "HTTP 500", status = 500) },
            awaitOnline = { true }, clockMillis = { testScheduler.currentTime })
        assertEquals(500, (runCatching { refused.resolve("ix", Resolution(allow = true)) }.exceptionOrNull() as HubFailure).status)
        assertEquals(1, attempts)

        // A 503 that says nothing was delivered is the hub refusing, not a proxy that dropped the request.
        attempts = 0
        val undelivered = HubChatTransport({ _, _, _, _ ->
            attempts++
            throw HubFailure(HubError.Server, "offline", status = 503, code = "assistant_offline", delivered = "none")
        }, awaitOnline = { true }, clockMillis = { testScheduler.currentTime })
        assertEquals(503, (runCatching { undelivered.resolve("ix", Resolution(allow = true)) }.exceptionOrNull() as HubFailure).status)
        assertEquals(1, attempts)
        assertFalse(HubChatTransport.cutOff(HubFailure(HubError.Server, "offline", status = 503, delivered = "none")))
        assertFalse(HubChatTransport.cutOff(HubFailure(HubError.Server, "offline", status = 503, code = "assistant_offline")))
        assertFalse(HubChatTransport.cutOff(HubFailure(HubError.Server, "in doubt", status = 502, delivered = "unknown")))
        assertTrue(HubChatTransport.cutOff(HubFailure(HubError.Server, "HTTP 503", status = 503)))
        val gone = HubChatTransport({ _, _, _, _ -> throw HubFailure(HubError.Server, "HTTP 410", status = 410) },
            awaitOnline = { true }, clockMillis = { testScheduler.currentTime })
        assertTrue(runCatching { gone.resolve("ix", Resolution(allow = true)) }.exceptionOrNull() is InteractionGoneException)
    }

    @Test fun aCardThatWasOverSaysHowItEnded() = runTest {
        fun respond(body: String) = HubChatTransport({ path, _, _, _ ->
            Response.Builder().request(Request.Builder().url("https://hub.example$path").build())
                .protocol(Protocol.HTTP_1_1).code(200).message("OK").body(body.toResponseBody()).build()
        }, awaitOnline = { true }, clockMillis = { testScheduler.currentTime })

        assertEquals(ResolveOutcome(), respond("""{"ok":true}""").resolveOutcome("ix", Resolution(allow = true)))
        assertEquals(ResolveOutcome(), respond("").resolveOutcome("ix", Resolution(allow = true)))
        val already = respond("""{"ok":true,"already":true,"status":"resolved","resolution":{"allow":false,"answered_by":"assistant"}}""")
            .resolveOutcome("ix", Resolution(allow = true))
        assertEquals(ResolveOutcome(true, "resolved", Resolution(allow = false, answeredBy = "assistant")), already)

        for (status in listOf("expired", "cancelled", "dismissed")) {
            val gone = HubChatTransport({ _, _, _, _ ->
                throw HubFailure(HubError.Server, "HTTP 410", status = 410, code = "gone",
                    body = """{"error":"interaction is no longer pending","code":"gone","status":"$status"}""")
            }, awaitOnline = { true }, clockMillis = { testScheduler.currentTime })
            val error = runCatching { gone.resolve("ix", Resolution(allow = true)) }.exceptionOrNull()
            assertEquals(status, (error as InteractionGoneException).status)
        }
        // A 410 that does not say (an older hub, or no record of how it ended).
        val unknown = HubChatTransport({ _, _, _, _ -> throw HubFailure(HubError.Server, "HTTP 410", status = 410, body = """{"error":"x","code":"gone"}""") },
            awaitOnline = { true }, clockMillis = { testScheduler.currentTime })
        assertNull((runCatching { unknown.resolve("ix", Resolution(allow = true)) }.exceptionOrNull() as InteractionGoneException).status)
    }
}
