package persona.core.data

import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.test.*
import org.junit.Assert.*
import org.junit.Test
import persona.core.contracts.*
import persona.core.contracts.api.*

@OptIn(kotlinx.coroutines.ExperimentalCoroutinesApi::class)
class CodexFirstMessageTest {
    @Test fun refusalsKeepTheMessageForManualRetryEvenWhenMarkedRetryable() = runTest {
        for (code in listOf("agent_stopped", "codex_not_ready", "codex_first_text", "input_draft", "dialog_open")) {
            val store = MemoryOutboxStore()
            val hub = Hub(MessageTransportResult.HubError(code, "none", retryable = true))
            val sender = OutboxMessageSender(HubScope("hub", code), store, hub, backgroundScope)
            sender.send(SessionKey("computer", "waiting"), "Review the changes", "message")
            runCurrent()
            advanceTimeBy(11_000)
            runCurrent()
            assertEquals(code, OutboxState.Refused, store.state("message"))
            assertEquals(code, 1, hub.requests)
            assertEquals("Review the changes", sender.outbox.value.single().text)
            sender.close()
        }
    }

    @Test fun anUnconfirmedFirstMessageNeverRetriesAsANewSend() = runTest {
        val store = MemoryOutboxStore()
        val hub = Hub(MessageTransportResult.HubError("codex_first_unconfirmed", "unknown", retryable = true))
        val sender = OutboxMessageSender(HubScope("hub", "pairing"), store, hub, backgroundScope)
        sender.send(SessionKey("computer", "waiting"), "Review the changes", "message")
        runCurrent()
        assertEquals(OutboxState.NeedsConfirmation, store.state("message"))
        advanceTimeBy(30_001)
        runCurrent()
        assertEquals(OutboxState.NeedsConfirmation, store.state("message"))
        assertEquals(1, hub.requests)
        sender.close()
    }

    @Test fun aFirstMessageSettlesAfterTheWaitingRowDisappears() = runTest {
        val waiting = SessionKey("computer", "waiting")
        val thread = SessionKey("computer", "thread")
        val store = MemoryOutboxStore()
        val sender = OutboxMessageSender(HubScope("hub", "pairing"), store,
            Hub(MessageTransportResult.Accepted), backgroundScope)
        var waitingPresent = true
        sender.connectActivity { it == waiting && waitingPresent }
        sender.send(waiting, "Review the changes", "message")
        runCurrent()
        assertEquals(OutboxState.Sent, store.state("message"))
        assertEquals(setOf(waiting), sender.awaitingTranscript.first())
        sender.observeTranscript(thread, listOf(Item("echo", 1, role = "user", kind = "text", text = "Review the changes")))
        assertEquals(OutboxState.Sent, store.state("message"))
        waitingPresent = false
        advanceTimeBy(60_001)
        runCurrent()
        assertEquals(OutboxState.Shown, store.state("message"))
        assertTrue(sender.outbox.value.isEmpty())
        assertTrue(sender.awaitingTranscript.first().isEmpty())
        sender.close()
    }

    private class Hub(private val result: MessageTransportResult) : MessageTransport {
        override val readyToSend = MutableStateFlow(true)
        override val supportsClientOps = true
        var requests = 0
        override suspend fun send(request: MessageRequest): MessageTransportResult { requests++; return result }
    }
}
