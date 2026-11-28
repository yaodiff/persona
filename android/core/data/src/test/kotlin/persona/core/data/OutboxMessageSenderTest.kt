package persona.core.data

import persona.core.contracts.HubScope
import persona.core.contracts.Item
import persona.core.contracts.SessionKey
import persona.core.contracts.api.MessageRequest
import persona.core.contracts.api.MessageEvidence
import persona.core.contracts.api.MessageTransport
import persona.core.contracts.api.MessageTransportResult
import persona.core.contracts.api.MainConversationResolver
import persona.core.contracts.api.MessageVia
import persona.core.contracts.api.PendingMainConversation
import persona.core.contracts.api.ChatComposerService
import persona.core.contracts.api.ComposerDelivery
import persona.core.contracts.api.DeliveryState
import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.test.runCurrent
import kotlinx.coroutines.test.runTest
import kotlinx.coroutines.test.advanceTimeBy
import kotlinx.coroutines.awaitCancellation
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.CoroutineStart
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.async
import kotlinx.coroutines.withContext
import kotlinx.coroutines.withTimeoutOrNull
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

@OptIn(ExperimentalCoroutinesApi::class)
class OutboxMessageSenderTest {
    private val scope = HubScope("test-hub", "generation-a")
    private val session = SessionKey("machine", "session")

    @Test fun composerUsesOriginalReadinessAndPersistsManualRefusalRetry() = runTest {
        val store = MemoryOutboxStore()
        val original = FakeTransport(online = false)
        val sender = OutboxMessageSender(scope, store, original, backgroundScope)
        var requests = 0
        sender.connectComposer { transport ->
            object : MessageTransport by transport, ChatComposerService {
                override val deliveries = MutableStateFlow(emptyMap<String, ComposerDelivery>())
                override suspend fun send(request: MessageRequest): MessageTransportResult {
                    requests++
                    return MessageTransportResult.HubError("input_draft", "none", retryable = false)
                }
                override suspend fun upload(session: SessionKey, name: String, file: java.io.File): String = error("Unused")
                override suspend fun interrupt(session: SessionKey) = Unit
                override suspend fun retryRefused(clientMessageId: String, clearDraft: Boolean): ComposerDelivery {
                    sender.recordComposerDelivery(clientMessageId, ComposerDelivery(DeliveryState.Sending))
                    return ComposerDelivery(DeliveryState.Sent).also { sender.recordComposerDelivery(clientMessageId, it) }
                }
            }
        }.also { composer ->
            sender.send(session, "保留原消息", "retry")
            runCurrent()
            assertEquals(0, requests)
            original.readyToSend.value = true
            runCurrent()
            assertEquals(1, requests)
            assertEquals(OutboxState.Refused, store.state("retry"))
            composer.retryRefused("retry", true)
            assertEquals(OutboxState.Sent, store.state("retry"))
            assertEquals("保留原消息", store.entries.value.single().text)
            assertTrue(original.requests.isEmpty())
        }
        sender.close()
    }

    @Test fun offlineFirstMessagesSurviveRestartAndBindToOneMainConversation() = runTest {
        val store = MemoryOutboxStore()
        val transport = FakeTransport(online = false)
        var creates = 0
        val resolver = object : MainConversationResolver {
            override suspend fun findMainConversation(): SessionKey? = null
            override suspend fun createMainConversation(): SessionKey { creates++; return session }
        }
        val first = OutboxMessageSender(scope, store, transport, backgroundScope)
        first.connectMainConversation(resolver)
        first.sendToMain("first", "main-a")
        first.sendToMain("shared context", "main-b")
        assertEquals(listOf("first", "shared context"), store.entries.value.map { it.text })
        assertTrue(store.entries.value.all { it.session == PendingMainConversation })
        runCurrent()
        assertEquals(0, creates)
        assertTrue(transport.requests.isEmpty())
        first.close()

        val restarted = OutboxMessageSender(scope, store, transport, backgroundScope)
        restarted.connectMainConversation(resolver)
        runCurrent()
        transport.answers += MessageTransportResult.NotSent
        transport.readyToSend.value = true
        runCurrent()
        assertEquals(1, creates)
        assertTrue(store.entries.value.all { it.session == session })
        assertEquals(OutboxState.WaitingForNetwork, store.state("main-a"))
        transport.readyToSend.value = false
        runCurrent()
        transport.answers += MessageTransportResult.Accepted
        transport.answers += MessageTransportResult.Accepted
        transport.readyToSend.value = true
        runCurrent()
        assertEquals(1, creates)
        assertTrue(store.entries.value.all { it.state == OutboxState.Sent })
        assertEquals(listOf("main-a", "main-a", "main-b"), transport.requests.map { it.clientMessageId })
        assertTrue(transport.requests.all { it.session == session })
        restarted.close()
    }

    @Test fun preparingAMainForAShareMarksCreationAndTheFollowingMessage() = runTest {
        val transport = FakeTransport(online = true)
        transport.answers += MessageTransportResult.Accepted
        val creations = mutableListOf<String?>()
        val resolver = object : MainConversationResolver {
            override suspend fun findMainConversation(): SessionKey? = null
            override suspend fun createMainConversation(): SessionKey = error("A marked creation must use its marked overload")
            override suspend fun createMainConversation(via: String?): SessionKey {
                creations += via
                return session
            }
        }
        val sender = OutboxMessageSender(scope, MemoryOutboxStore(), transport, backgroundScope)
        sender.connectMainConversation(resolver)
        assertEquals(session, sender.prepareMainConversation(MessageVia.Share))
        sender.sendToMain("Shared words", "share-main", null, MessageVia.Share)
        runCurrent()
        assertEquals(listOf(MessageVia.Share), creations)
        assertEquals(listOf(MessageVia.Share), transport.requests.map { it.via })
        sender.close()
    }

    @Test fun anOfflineShareRestoresProvenanceIntoCreationAndMessage() = runTest {
        val store = MemoryOutboxStore()
        val transport = FakeTransport(online = false)
        val first = OutboxMessageSender(scope, store, transport, backgroundScope)
        first.sendToMain("Shared words", "share-main", null, MessageVia.Share)
        runCurrent()
        assertEquals(MessageVia.Share, store.entries.value.single().via)
        assertTrue(transport.requests.isEmpty())
        first.close()

        val creations = mutableListOf<String?>()
        val resolver = object : MainConversationResolver {
            override suspend fun findMainConversation(): SessionKey? = null
            override suspend fun createMainConversation(): SessionKey = error("A marked creation must use its marked overload")
            override suspend fun createMainConversation(via: String?): SessionKey {
                creations += via
                return session
            }
        }
        val restarted = OutboxMessageSender(scope, store, transport, backgroundScope)
        restarted.connectMainConversation(resolver)
        transport.answers += MessageTransportResult.Accepted
        transport.readyToSend.value = true
        runCurrent()
        assertEquals(listOf(MessageVia.Share), creations)
        assertEquals(listOf(MessageVia.Share), transport.requests.map { it.via })
        restarted.close()
    }

    /** A start the hub refuses for a missing step (409 no_machine): the message is refused, not queued, and a retry starts it. */
    @Test fun refusedMainStartWaitsForTheUserAndARetryStartsIt() = runTest {
        val store = MemoryOutboxStore()
        val transport = FakeTransport(online = true)
        var creates = 0
        var computer = false
        val resolver = object : MainConversationResolver {
            override suspend fun findMainConversation(): SessionKey? = null
            override suspend fun createMainConversation(): SessionKey {
                creates++
                if (!computer) throw Refusal(409, "no_machine")
                return session
            }
        }
        val sender = OutboxMessageSender(scope, store, transport, backgroundScope)
        sender.connectMainConversation(resolver)
        sender.sendToMain("你好", "main-a")
        runCurrent()
        assertEquals(1, creates)
        assertEquals(OutboxState.Refused, store.state("main-a"))
        assertEquals("no_machine", store.entries.value.single().error)
        assertTrue(transport.requests.isEmpty())
        // Not tried again by itself: nothing comes later as a surprise answer.
        transport.readyToSend.value = false; runCurrent(); transport.readyToSend.value = true; runCurrent()
        assertEquals(1, creates)

        computer = true
        transport.answers += MessageTransportResult.Accepted
        assertTrue(sender.retry("main-a"))
        runCurrent()
        assertEquals(2, creates)
        assertEquals(OutboxState.Sent, store.state("main-a"))
        assertEquals(session, transport.requests.single().session)
        sender.close()
    }

    /** Any other failure of the start may have created it on the hub: it is looked up, never created twice. */
    @Test fun aStartFailureWithoutASetupCodeKeepsTheClaim() = runTest {
        val store = MemoryOutboxStore()
        val transport = FakeTransport(online = true)
        var creates = 0
        val resolver = object : MainConversationResolver {
            override suspend fun findMainConversation(): SessionKey? = null
            override suspend fun createMainConversation(): SessionKey { creates++; throw Refusal(502, "internal") }
        }
        val sender = OutboxMessageSender(scope, store, transport, backgroundScope)
        sender.connectMainConversation(resolver)
        sender.sendToMain("你好", "main-a")
        runCurrent()
        assertEquals(OutboxState.WaitingForNetwork, store.state("main-a"))
        transport.readyToSend.value = false; runCurrent(); transport.readyToSend.value = true; runCurrent()
        assertEquals(1, creates)
        sender.close()
    }

    /** The computer asleep (502 node_unreachable) or the assistant restarting (503): it is started once they are back. */
    @Test fun aStartRefusedWhileOfflineIsStartedLater() = runTest {
        for ((status, code) in listOf(502 to "node_unreachable", 503 to "assistant_offline")) {
            val store = MemoryOutboxStore()
            val transport = FakeTransport(online = false)
            transport.answers += MessageTransportResult.Accepted
            var creates = 0
            var looks = 0
            val resolver = object : MainConversationResolver {
                override suspend fun findMainConversation(): SessionKey? { looks++; return null }
                override suspend fun createMainConversation(): SessionKey {
                    creates++
                    if (creates == 1) throw Refusal(status, code)
                    return SessionKey("machine", "main")
                }
            }
            val sender = OutboxMessageSender(scope, store, transport, backgroundScope)
            sender.connectMainConversation(resolver)
            sender.sendToMain("你好", "main-$code")
            runCurrent()
            transport.readyToSend.value = true; runCurrent()
            assertEquals(code, 1, creates)
            assertEquals(OutboxState.WaitingForNetwork, store.state("main-$code"))
            transport.readyToSend.value = false; runCurrent(); transport.readyToSend.value = true; runCurrent()
            assertEquals(code, 2, creates)
            assertEquals(code, 2, looks)
            assertEquals(1, transport.requests.size)
            sender.close()
        }
    }

    private class Refusal(override val status: Int?, override val code: String?) :
        java.io.IOException(code), persona.core.contracts.api.HubCodedFailure

    @Test fun lostCreateReceiptNeverCreatesAgainAndRetainsInputUntilMainIsFound() = runTest {
        val store = MemoryOutboxStore()
        val transport = FakeTransport(online = true)
        var creates = 0
        var remoteMain: SessionKey? = null
        val resolver = object : MainConversationResolver {
            override suspend fun findMainConversation(): SessionKey? = remoteMain
            override suspend fun createMainConversation(): SessionKey {
                creates++
                throw java.io.IOException("Receipt lost")
            }
        }
        val first = OutboxMessageSender(scope, store, transport, backgroundScope)
        first.connectMainConversation(resolver)
        first.sendToMain("keep this input", "main-a")
        runCurrent()
        assertEquals(1, creates)
        assertEquals("keep this input", store.entries.value.single().text)
        assertTrue(transport.requests.isEmpty())
        first.close()

        val restarted = OutboxMessageSender(scope, store, transport, backgroundScope)
        restarted.connectMainConversation(resolver)
        runCurrent()
        assertEquals(1, creates)
        assertTrue(transport.requests.isEmpty())
        transport.readyToSend.value = false
        runCurrent()
        remoteMain = session
        transport.answers += MessageTransportResult.Accepted
        transport.readyToSend.value = true
        runCurrent()
        assertEquals(1, creates)
        assertEquals(session, transport.requests.single().session)
        assertEquals("keep this input", transport.requests.single().text)
        assertEquals(OutboxState.Sent, store.state("main-a"))
        restarted.close()
    }

    /** "New main chat": once the old main conversation is put away, the next message starts a new one, not the archived one. */
    @Test fun aReplacedMainConversationIsForgottenAndTheNextMessageStartsANewOne() = runTest {
        val store = MemoryOutboxStore()
        val transport = FakeTransport(online = true)
        val replacement = SessionKey("machine", "next-main")
        var remoteMain: SessionKey? = session
        var creates = 0
        val resolver = object : MainConversationResolver {
            override suspend fun findMainConversation(): SessionKey? = remoteMain
            override suspend fun createMainConversation(): SessionKey { creates++; return replacement.also { remoteMain = it } }
        }
        val sender = OutboxMessageSender(scope, store, transport, backgroundScope)
        sender.connectMainConversation(resolver)
        transport.answers += MessageTransportResult.Accepted
        sender.sendToMain("to the old one", "old")
        runCurrent()
        assertEquals(session, transport.requests.single().session)

        remoteMain = null // the hub put it away
        sender.mainReplaced(SessionKey("machine", "someone-else")) // not the bound one: nothing changes
        assertEquals(session, store.mainConversation())
        sender.mainReplaced(session)
        assertEquals(null, store.mainConversation())
        transport.answers += MessageTransportResult.Accepted
        sender.sendToMain("to the new one", "new")
        runCurrent()
        assertEquals(1, creates)
        assertEquals(replacement, transport.requests.last().session)
        assertEquals(OutboxState.Sent, store.state("new"))
        sender.close()
    }

    /**
     * Settings › Assistant "Change machine" clears the hub's main conversation (`PUT {"machine_id","main":"-"}`); a binding kept from before
     * (the app restarted, another device moved it) must not send the next main message to the old machine's
     * conversation, which is an ordinary one now.
     */
    @Test fun aMainConversationTheHubNoLongerHasIsDroppedOnCatchUp() = runTest {
        val store = MemoryOutboxStore()
        val transport = FakeTransport(online = true)
        val old = SessionKey("old-machine", "old-main")
        val moved = SessionKey("new-machine", "new-main")
        var remoteMain: SessionKey? = old
        var creates = 0
        val resolver = object : MainConversationResolver {
            override suspend fun findMainConversation(): SessionKey? = remoteMain
            override suspend fun createMainConversation(): SessionKey { creates++; return moved.also { remoteMain = it } }
        }
        val sender = OutboxMessageSender(scope, store, transport, backgroundScope)
        sender.connectMainConversation(resolver)
        transport.answers += MessageTransportResult.Accepted
        sender.sendToMain("before the move", "before")
        runCurrent()
        assertEquals(old, transport.requests.single().session)

        remoteMain = null // moved to new-machine; the hub has no main conversation until the next message
        sender.onReady()
        transport.answers += MessageTransportResult.Accepted
        sender.sendToMain("after the move", "after")
        runCurrent()
        assertEquals(1, creates)
        assertEquals(moved, transport.requests.last().session)
        assertEquals(moved, store.mainConversation())
        sender.close()
    }

    /** mainChanged asks the hub: the binding stays while the hub names it or cannot be asked, and goes once it names another. */
    @Test fun mainChangedKeepsTheBindingUnlessTheHubNamesAnotherOne() = runTest {
        val store = MemoryOutboxStore()
        val transport = FakeTransport(online = true)
        val other = SessionKey("machine", "other-main")
        var remoteMain: SessionKey? = session
        var failing = false
        var hanging = false
        var creates = 0
        val resolver = object : MainConversationResolver {
            override suspend fun findMainConversation(): SessionKey? {
                if (hanging) awaitCancellation()
                if (failing) throw java.io.IOException("hub unreachable")
                return remoteMain
            }
            override suspend fun createMainConversation(): SessionKey { creates++; error("Unused") }
        }
        val sender = OutboxMessageSender(scope, store, transport, backgroundScope)
        sender.mainChanged() // nothing bound, no resolver: nothing to do
        sender.connectMainConversation(resolver)
        transport.answers += MessageTransportResult.Accepted
        sender.sendToMain("first", "first")
        runCurrent()
        assertEquals(session, store.mainConversation())

        sender.mainChanged()
        assertEquals("the hub still names it", session, store.mainConversation())
        failing = true
        sender.mainChanged()
        assertEquals("the hub could not be asked", session, store.mainConversation())
        failing = false
        hanging = true
        sender.mainChanged() // times out after reconciliationTimeoutMillis
        assertEquals("a lookup that timed out is not the hub saying there is none", session, store.mainConversation())
        hanging = false

        remoteMain = other // another device replaced it
        sender.mainChanged()
        assertEquals(null, store.mainConversation())
        transport.answers += MessageTransportResult.Accepted
        sender.sendToMain("second", "second")
        runCurrent()
        assertEquals(0, creates)
        assertEquals(other, transport.requests.last().session)
        assertEquals(other, store.mainConversation())
        sender.close()
    }

    @Test fun managerCustodySurvivesRestartWithoutSendingAgainAndRequiresAnIdentifiedEcho() = runTest {
        val store = MemoryOutboxStore()
        val transport = FakeTransport(online = true, supportsClientOps = true)
        transport.answers += MessageTransportResult.Held
        val at = java.time.Instant.parse("2026-11-01T10:00:00Z")
        val first = OutboxMessageSender(scope, store, transport, backgroundScope) { at.toEpochMilli() }
        first.send(session, "Held text", "held-a")
        runCurrent()
        assertEquals(OutboxState.Held, store.state("held-a"))
        assertTrue(first.outbox.value.isEmpty())
        first.close()

        val resumed = OutboxMessageSender(scope, store, transport, backgroundScope) { at.toEpochMilli() }
        transport.evidence["held-a"] = MessageEvidence.Receipt(MessageTransportResult.Held)
        runCurrent()
        resumed.onReady()
        assertFalse(resumed.retry("held-a"))
        assertFalse(resumed.cancel("held-a"))
        val sameWords = Item("other", 3, role = "user", kind = "text", text = "Held text", ts = at.toString())
        resumed.observeTranscript(session, listOf(sameWords))
        assertEquals(OutboxState.Held, store.state("held-a"))
        resumed.observeTranscript(session, listOf(sameWords.copy(id = "landed", clientMsgId = "held-a")))
        assertEquals(OutboxState.Shown, store.state("held-a"))
        assertEquals(1, transport.requests.size)
        resumed.close()
    }

    @Test fun durableQueueWaitsForNetworkAndDistinguishesRefusalFromUncertainDelivery() = runTest {
        val store = MemoryOutboxStore()
        val transport = FakeTransport(online = false)
        val sender = OutboxMessageSender(scope, store, transport, backgroundScope) { 100 }
        sender.send(session, "hello", "message-a", null)
        // send() returns only after persistence, without waiting for the network.
        assertEquals(OutboxState.Queued, store.state("message-a"))
        runCurrent()
        assertEquals(OutboxState.WaitingForNetwork, store.state("message-a"))
        assertTrue(transport.requests.isEmpty())

        transport.answers += MessageTransportResult.NotSent
        transport.readyToSend.value = true
        runCurrent()
        assertEquals(OutboxState.WaitingForNetwork, store.state("message-a"))
        transport.readyToSend.value = false
        runCurrent()
        transport.answers += MessageTransportResult.HubError("input_draft", "none")
        transport.readyToSend.value = true
        runCurrent()
        assertEquals(OutboxState.Refused, store.state("message-a"))

        transport.answers += MessageTransportResult.HubError(delivered = "unknown")
        sender.send(session, "next", "message-b", null)
        runCurrent()
        assertEquals(OutboxState.Verifying, store.state("message-b"))
        sender.observeTranscript(session, listOf(Item("message-b", 1, role = "user", kind = "text", text = "next")))
        runCurrent()
        assertEquals(OutboxState.Shown, store.state("message-b"))
        assertEquals(OutboxState.Unknown, OutboxMessageSender.classify(MessageTransportResult.HubError()))
        sender.close()
    }

    @Test fun keptMessageLeavesTheLocalOutboxForTheSessionRow() = runTest {
        val store = MemoryOutboxStore()
        val transport = FakeTransport(online = true)
        transport.answers += MessageTransportResult.Kept
        val sender = OutboxMessageSender(scope, store, transport, backgroundScope)
        sender.send(session, "keep this for the assistant", "kept")
        runCurrent()

        assertEquals(1, transport.requests.size)
        assertTrue("the authoritative session row draws a kept message instead",
            sender.outbox.value.none { it.clientMessageId == "kept" })
        assertEquals("kept", sender.sendReceipts.value.single().state)
        assertFalse(sender.sendReceipts.value.single().processing)
        advanceTimeBy(60_000)
        runCurrent()
        assertEquals("kept is not delivered and must not be sent again", 1, transport.requests.size)
        assertTrue(sender.outbox.value.none { it.clientMessageId == "kept" })
        sender.close()
    }

    @Test fun keptComposerRetryLeavesTheSendingOutboxForTheSessionRow() = runTest {
        val store = MemoryOutboxStore()
        store.enqueue(OutboxEntry("kept-retry", session, "keep this", state = OutboxState.Refused, createdAt = 100))
        val sender = OutboxMessageSender(scope, store, FakeTransport(online = false), backgroundScope)
        sender.recordComposerDelivery("kept-retry", ComposerDelivery(DeliveryState.Sending), "retry-op", null)
        assertEquals(OutboxState.Sending, store.state("kept-retry"))

        sender.recordComposerDelivery("kept-retry", ComposerDelivery(DeliveryState.Sending), "retry-op", MessageTransportResult.Kept)
        assertEquals(OutboxState.Kept, store.state("kept-retry"))
        assertTrue(sender.outbox.value.none { it.clientMessageId == "kept-retry" })
        sender.close()
    }

    @Test fun aLateComposerReplyKeepsANewerRetryAndAnOwnerDiscard() = runTest {
        val store = MemoryOutboxStore()
        val sender = OutboxMessageSender(scope, store, FakeTransport(online = false), backgroundScope)
        runCurrent()
        store.enqueue(OutboxEntry("visible", session, "hello", state = OutboxState.Refused, createdAt = 100))
        val sending = ComposerDelivery(DeliveryState.Sending)
        val sent = ComposerDelivery(DeliveryState.Sent)
        sender.recordComposerDelivery("visible", sending, "first")
        assertEquals("the live attempt is bound to its own operation", "first", store.entries.value.single().operationId)
        sender.recordComposerDelivery("visible", ComposerDelivery(DeliveryState.Refused), "first", MessageTransportResult.NotSent)
        sender.recordComposerDelivery("visible", sending, "second")
        sender.recordComposerDelivery("visible", sent, "first", MessageTransportResult.Accepted)
        assertEquals(OutboxState.Sending, store.state("visible"))
        assertEquals("second", store.entries.value.single().operationId)

        sender.recordComposerDelivery("visible", ComposerDelivery(DeliveryState.Unknown), "second", MessageTransportResult.NoReceipt)
        assertTrue(sender.discard("visible"))
        sender.recordComposerDelivery("visible", sent, "second", MessageTransportResult.Accepted)
        assertEquals(OutboxState.Cancelled, store.state("visible"))
        sender.close()
    }

    @Test fun automaticRetryDoesNotAdoptAnActiveComposerAttempt() = runTest {
        val store = InterleavedOutboxStore()
        store.enqueue(OutboxEntry("visible", session, "hello", state = OutboxState.Refused, createdAt = 100))
        val automaticStarted = CompletableDeferred<Unit>()
        val automaticFinished = CompletableDeferred<Unit>()
        val transport = object : MessageTransport {
            override val readyToSend = MutableStateFlow(true)
            override val supportsClientOps = true
            override suspend fun send(request: MessageRequest): MessageTransportResult {
                automaticStarted.complete(Unit)
                automaticFinished.await()
                return MessageTransportResult.NoReceipt
            }
        }
        val sender = OutboxMessageSender(scope, store, transport, backgroundScope)
        runCurrent()

        // Arm the pause before the automatic attempt starts: while withContext(Dispatchers.Default) waits for a
        // worker thread, runTest can advance virtual time past the send timeouts and review deadline. Holding the
        // first durable Unknown write up front keeps the interleaving deterministic under scheduling delay.
        store.interleaveManual = true
        val automatic = async { sender.retry("visible") }
        val manual = withContext(Dispatchers.Default) {
            withTimeoutOrNull(10_000) { automaticStarted.await() } ?: error("Automatic send did not start")
            // Observe the automatic Queued→Sending transition itself, not an arbitrary Sending row left by a later
            // operation. Only the subsequent durable Unknown write is held for the composer claim.
            withTimeoutOrNull(10_000) { store.automaticSendingWritten.await() } ?:
                error("Automatic retry did not enter Sending; transitions=${store.transitions} state=${store.entries.value}")
            withTimeoutOrNull(10_000) { store.automaticStateWritten.await() } ?: error("Automatic transition did not start")
            async(start = CoroutineStart.UNDISPATCHED) { sender.beginComposerRetry("visible", "manual-operation") }.also {
                store.continueAutomatic.complete(Unit)
                automaticFinished.complete(Unit)
            }
        }
        withTimeoutOrNull(10_000) { manual.join() } ?: error("Manual retry did not finish")
        withTimeoutOrNull(10_000) { automatic.join() } ?: error("Automatic retry did not finish")
        runCurrent()

        assertEquals(OutboxState.Sending, store.entries.value.single { it.clientMessageId == "visible" }.state)
        assertEquals("manual-operation", store.entries.value.single().operationId)
        sender.recordComposerDelivery("visible", ComposerDelivery(DeliveryState.Sent), "manual-operation", MessageTransportResult.Accepted)
        assertEquals(OutboxState.Sent, store.entries.value.single().state)
        sender.close()
        runCurrent()
    }

    @Test fun reviewDeadlineDoesNotOverwriteAComposerRetryClaimedDuringWrite() = runTest {
        val store = DeadlineInterleavedOutboxStore()
        store.enqueue(OutboxEntry("visible", session, "hello", state = OutboxState.Unknown, createdAt = 100))
        val sender = OutboxMessageSender(
            scope,
            store,
            FakeTransport(online = false, supportsClientOps = true),
            backgroundScope,
            reviewTimeoutMillis = 1,
        )
        runCurrent()

        val manual = withContext(Dispatchers.Default) {
            withTimeoutOrNull(10_000) { store.deadlineWriteStarted.await() } ?:
                error("Review deadline write did not start; transitions=${store.transitions}")
            async(start = CoroutineStart.UNDISPATCHED) { sender.beginComposerRetry("visible", "manual-operation") }.also {
                store.continueDeadline.complete(Unit)
            }
        }
        withTimeoutOrNull(10_000) { manual.join() } ?:
            error("Manual retry did not finish; transitions=${store.transitions} state=${store.entries.value}")
        runCurrent()

        val current = store.entries.value.single { it.clientMessageId == "visible" }
        assertEquals(OutboxState.Sending, current.state)
        assertEquals("manual-operation", current.operationId)
        sender.recordComposerDelivery("visible", ComposerDelivery(DeliveryState.Sent), "manual-operation", MessageTransportResult.Accepted)
        assertEquals(OutboxState.Sent, store.entries.value.single().state)
        sender.close()
        runCurrent()
    }

    /** Pauses the review-deadline Unknown→NeedsConfirmation write while a manual retry claims the message. */
    private class DeadlineInterleavedOutboxStore : OutboxStore {
        val deadlineWriteStarted = CompletableDeferred<Unit>()
        val continueDeadline = CompletableDeferred<Unit>()
        val transitions = mutableListOf<Pair<Set<OutboxState>, OutboxState>>()
        private val delegate = MemoryOutboxStore()
        override val entries = delegate.entries
        override suspend fun enqueue(entry: OutboxEntry) = delegate.enqueue(entry)
        override suspend fun recoverInterrupted() = delegate.recoverInterrupted()
        override suspend fun transition(id: String, from: Set<OutboxState>, to: OutboxState, error: String?): Boolean {
            transitions += from to to
            if (to == OutboxState.NeedsConfirmation) {
                deadlineWriteStarted.complete(Unit)
                continueDeadline.await()
            }
            return delegate.transition(id, from, to, error)
        }
        override suspend fun transition(id: String, from: Set<OutboxState>, to: OutboxState, error: String?, opId: String): Boolean =
            delegate.transition(id, from, to, error, opId)
    }

    @Test fun automaticImmediateRetryDoesNotOverwriteAComposerOperation() = runTest {
        val store = ImmediateRetryInterleavedOutboxStore()
        store.enqueue(OutboxEntry("visible", session, "hello", state = OutboxState.Refused, createdAt = 100))
        val transport = object : MessageTransport {
            override val readyToSend = MutableStateFlow(true)
            override val supportsClientOps = true
            override suspend fun send(request: MessageRequest): MessageTransportResult {
                if (request.headers["X-Persona-Op-Id"] == "manual-operation") error("The automatic attempt adopted the composer operation")
                return MessageTransportResult.NoReceipt
            }
        }
        val sender = OutboxMessageSender(scope, store, transport, backgroundScope, reviewTimeoutMillis = 1)
        runCurrent()

        val automatic = async { sender.retry("visible") }
        val manual = withContext(Dispatchers.Default) {
            withTimeoutOrNull(10_000) { store.automaticRetryWritten.await() } ?:
                error("Automatic immediate retry did not start; transitions=${store.transitions}")
            async(start = CoroutineStart.UNDISPATCHED) { sender.beginComposerRetry("visible", "manual-operation") }.also {
                store.continueAutomatic.complete(Unit)
            }
        }
        withTimeoutOrNull(10_000) { manual.join() } ?:
            error("Manual retry did not finish; transitions=${store.transitions} state=${store.entries.value}")
        withTimeoutOrNull(10_000) { automatic.join() } ?:
            error("Automatic retry did not finish; transitions=${store.transitions} state=${store.entries.value}")
        runCurrent()

        val current = store.entries.value.single { it.clientMessageId == "visible" }
        assertEquals(OutboxState.Sending, current.state)
        assertEquals("manual-operation", current.operationId)
        sender.recordComposerDelivery("visible", ComposerDelivery(DeliveryState.Sent), "manual-operation", MessageTransportResult.Accepted)
        assertEquals(OutboxState.Sent, store.entries.value.single().state)
        sender.close()
        runCurrent()
    }

    /** Pauses the same-attempt Unknown→Sending write after a manual claim, not the earlier settlement. */
    private class ImmediateRetryInterleavedOutboxStore : OutboxStore {
        val automaticRetryWritten = CompletableDeferred<Unit>()
        val continueAutomatic = CompletableDeferred<Unit>()
        val transitions = mutableListOf<Pair<Set<OutboxState>, OutboxState>>()
        private val delegate = MemoryOutboxStore()
        override val entries = delegate.entries
        override suspend fun enqueue(entry: OutboxEntry) = delegate.enqueue(entry)
        override suspend fun recoverInterrupted() = delegate.recoverInterrupted()
        override suspend fun transition(id: String, from: Set<OutboxState>, to: OutboxState, error: String?): Boolean {
            transitions += from to to
            if (from == setOf(OutboxState.Unknown) && to == OutboxState.Sending) {
                automaticRetryWritten.complete(Unit)
                continueAutomatic.await()
            }
            return delegate.transition(id, from, to, error)
        }
        override suspend fun transition(id: String, from: Set<OutboxState>, to: OutboxState, error: String?, opId: String): Boolean =
            delegate.transition(id, from, to, error, opId)
    }

    private class InterleavedOutboxStore : OutboxStore {
        val automaticStateWritten = CompletableDeferred<Unit>()
        val automaticSendingWritten = CompletableDeferred<Unit>()
        val continueAutomatic = CompletableDeferred<Unit>()
        val transitions = mutableListOf<Pair<Set<OutboxState>, OutboxState>>()
        @Volatile var interleaveManual = false
        private val delegate = MemoryOutboxStore()
        override val entries = delegate.entries
        override suspend fun enqueue(entry: OutboxEntry) = delegate.enqueue(entry)
        override suspend fun recoverInterrupted() = delegate.recoverInterrupted()

        override suspend fun transition(id: String, from: Set<OutboxState>, to: OutboxState, error: String?): Boolean {
            transitions += from to to
            if (to == OutboxState.Sending && OutboxState.Queued in from) automaticSendingWritten.complete(Unit)
            if (to == OutboxState.Unknown) {
                automaticStateWritten.complete(Unit)
                if (interleaveManual) {
                    interleaveManual = false
                    continueAutomatic.await()
                }
            }
            return delegate.transition(id, from, to, error)
        }

        override suspend fun transition(id: String, from: Set<OutboxState>, to: OutboxState, error: String?, opId: String): Boolean =
            delegate.transition(id, from, to, error, opId)
    }

    @Test fun catchUpChecksAllUncertainStatesBeforeSendingAndUsesTheSameOperationId() = runTest {
        val store = MemoryOutboxStore()
        listOf(OutboxState.Sending, OutboxState.Unknown, OutboxState.Verifying).forEachIndexed { index, state ->
            store.enqueue(OutboxEntry("uncertain-$index", session, "text-$index", state = state, createdAt = 100))
        }
        store.enqueue(OutboxEntry("next", session, "next", createdAt = 101))
        val transport = FakeTransport(online = false, supportsClientOps = true)
        transport.evidence["uncertain-0"] = MessageEvidence.Receipt(MessageTransportResult.Accepted)
        transport.evidence["uncertain-1"] = MessageEvidence.Shown
        val sender = OutboxMessageSender(scope, store, transport, backgroundScope)
        runCurrent()
        sender.onReady()
        assertEquals(listOf("uncertain-0", "uncertain-1", "uncertain-2"), transport.lookups)
        assertTrue(transport.requests.isEmpty())
        assertEquals(OutboxState.Sent, store.state("uncertain-0"))
        assertEquals(OutboxState.Shown, store.state("uncertain-1"))
        transport.answers += MessageTransportResult.Accepted
        transport.answers += MessageTransportResult.Accepted
        transport.readyToSend.value = true
        runCurrent()
        assertEquals(listOf("uncertain-2", "next"), transport.requests.map { it.clientMessageId })
        assertEquals("uncertain-2", transport.requests.first().headers["X-Persona-Op-Id"])
        sender.close()
    }

    @Test fun legacyUnknownDoesNotBlockLaterMessagesAndTimesOutToManualDisposition() = runTest {
        val store = MemoryOutboxStore()
        val transport = FakeTransport(online = false)
        val sender = OutboxMessageSender(scope, store, transport, backgroundScope,
            reviewTimeoutMillis = 100, reconciliationTimeoutMillis = 10)
        sender.send(session, "cancel", "cancel")
        assertTrue(sender.cancel("cancel"))
        sender.send(session, "unknown", "unknown")
        sender.send(session, "later", "later")
        transport.answers += MessageTransportResult.NoReceipt
        transport.answers += MessageTransportResult.Accepted
        transport.readyToSend.value = true
        runCurrent()
        assertEquals(listOf("unknown", "later"), transport.requests.map { it.clientMessageId })
        assertTrue(!sender.cancel("unknown"))
        assertTrue(!sender.retry("unknown"))
        // A hung reconciliation is bounded and cannot hide the manual review deadline.
        transport.hangLookup = true
        transport.readyToSend.value = false
        runCurrent()
        transport.readyToSend.value = true
        runCurrent()
        advanceTimeBy(111)
        runCurrent()
        assertEquals(OutboxState.NeedsConfirmation, store.state("unknown"))
        assertEquals(2, transport.requests.size)
        assertTrue(sender.confirm("unknown", delivered = false))
        assertEquals(OutboxState.Refused, store.state("unknown"))
        transport.hangLookup = false
        transport.answers += MessageTransportResult.NoReceipt
        assertTrue(sender.retry("unknown"))
        runCurrent()
        assertTrue(sender.confirm("unknown", delivered = true))
        runCurrent()
        assertEquals(OutboxState.Sent, store.state("unknown"))
        assertTrue(sender.outbox.value.none { it.clientMessageId == "cancel" })
        sender.close()
    }

    @Test fun messagesOfUnknownDeliveryCanBeDiscardedAndAreNeverRetried() = runTest {
        val store = MemoryOutboxStore()
        val transport = FakeTransport(online = true)
        val sender = OutboxMessageSender(scope, store, transport, backgroundScope,
            reviewTimeoutMillis = 100, reconciliationTimeoutMillis = 10)
        transport.answers += MessageTransportResult.NoReceipt
        transport.answers += MessageTransportResult.Accepted
        sender.send(session, "unknown", "unknown")
        sender.send(session, "sent", "sent")
        runCurrent()
        assertTrue(store.state("unknown") in setOf(OutboxState.Unknown, OutboxState.Verifying))
        assertTrue(!sender.discard("sent"))
        assertTrue(sender.discard("unknown"))
        assertEquals(OutboxState.Cancelled, store.state("unknown"))
        advanceTimeBy(111)
        runCurrent()
        // Discarded: no review deadline brings it back, and it is never sent again.
        assertEquals(OutboxState.Cancelled, store.state("unknown"))
        assertTrue(sender.outbox.value.none { it.clientMessageId == "unknown" })
        assertEquals(listOf("unknown", "sent"), transport.requests.map { it.clientMessageId })
        sender.close()
    }

    @Test fun refusedMessagesCanBeDiscardedButSentOnesCannot() = runTest {
        val store = MemoryOutboxStore()
        val transport = FakeTransport(online = true)
        val sender = OutboxMessageSender(scope, store, transport, backgroundScope)
        transport.answers += MessageTransportResult.HubError(code = "input_draft", delivered = "none")
        transport.answers += MessageTransportResult.Accepted
        sender.send(session, "refused", "refused")
        runCurrent()
        assertEquals(OutboxState.Refused, store.state("refused"))
        sender.send(session, "sent", "sent")
        runCurrent()
        assertEquals(OutboxState.Sent, store.state("sent"))
        // Nothing of a refused message reached the agent, so deleting it is safe; a sent one stays to be confirmed.
        assertTrue(sender.cancel("refused"))
        assertTrue(!sender.cancel("sent"))
        assertEquals(OutboxState.Cancelled, store.state("refused"))
        runCurrent()
        assertTrue(sender.outbox.value.none { it.clientMessageId == "refused" })
        assertEquals(listOf("refused", "sent"), transport.requests.map { it.clientMessageId })
        sender.close()
    }

    @Test fun undoPutsADeletedMessageBackAsItWas() = runTest {
        val store = MemoryOutboxStore()
        val transport = FakeTransport(online = true)
        val sender = OutboxMessageSender(scope, store, transport, backgroundScope)
        transport.answers += MessageTransportResult.HubError(code = "input_draft", delivered = "none")
        sender.send(session, "refused", "refused")
        runCurrent()
        val refused = sender.outbox.value.single { it.clientMessageId == "refused" }
        assertEquals("input_draft", refused.error)
        assertTrue(sender.cancel("refused"))
        runCurrent()
        assertTrue(sender.outbox.value.none { it.clientMessageId == "refused" })
        // Back as it was, its refusal too, and nothing sent for it.
        sender.restore(refused)
        runCurrent()
        assertEquals(refused, sender.outbox.value.single { it.clientMessageId == "refused" })
        // A second Undo, or one for a message no longer deleted, changes nothing.
        sender.restore(refused.copy(state = DeliveryState.Queued))
        runCurrent()
        assertEquals(OutboxState.Refused, store.state("refused"))
        assertEquals(listOf("refused"), transport.requests.map { it.clientMessageId })

        // One still waiting comes back waiting and goes once the link is up.
        transport.readyToSend.value = false
        sender.send(session, "waiting", "waiting")
        runCurrent()
        val waiting = sender.outbox.value.single { it.clientMessageId == "waiting" }
        assertEquals(DeliveryState.WaitingForNetwork, waiting.state)
        assertTrue(sender.cancel("waiting"))
        sender.restore(waiting)
        runCurrent()
        assertEquals(OutboxState.WaitingForNetwork, store.state("waiting"))
        transport.answers += MessageTransportResult.Accepted
        transport.readyToSend.value = true
        runCurrent()
        assertEquals(listOf("refused", "waiting"), transport.requests.map { it.clientMessageId })
        sender.close()
    }

    /**
     * An agy open in a terminal (409 `agy_in_terminal`, delivered none, #16531): the refused message keeps the hub's
     * sentence with its code, and Undo puts both back; a retry leaves none of it. A refusal without words, a bare status,
     * the code itself, an internal failure's detail, a refusal without a code and a wait keep no words.
     */
    @Test fun aRefusalKeepsTheHubsWordsWithItsCode() = runTest {
        val words = "This Antigravity conversation is open in agy in a terminal on that computer. Nothing was sent."
        val store = MemoryOutboxStore()
        val transport = FakeTransport(online = true)
        val sender = OutboxMessageSender(scope, store, transport, backgroundScope)
        transport.answers += MessageTransportResult.HubError("agy_in_terminal", "none", words, retryable = false)
        sender.send(session, "to agy", "agy")
        runCurrent()
        val refused = sender.outbox.value.single { it.clientMessageId == "agy" }
        assertEquals(DeliveryState.Refused, refused.state)
        assertEquals("agy_in_terminal", refused.error)
        assertEquals(words, refused.reason)
        assertTrue(sender.cancel("agy"))
        runCurrent()
        sender.restore(refused)
        runCurrent()
        assertEquals(refused, sender.outbox.value.single { it.clientMessageId == "agy" })
        transport.answers += MessageTransportResult.Accepted
        assertTrue(sender.retry("agy"))
        runCurrent()
        assertEquals(OutboxState.Sent, store.state("agy"))
        assertEquals(null, store.entries.value.single { it.clientMessageId == "agy" }.reason)

        val cases = listOf(
            "no words" to MessageTransportResult.HubError("agy_in_terminal", "none", retryable = false),
            "a bare status" to MessageTransportResult.HubError("agy_in_terminal", "none", "HTTP 409", retryable = false),
            "the code itself" to MessageTransportResult.HubError("agy_in_terminal", "none", "agy_in_terminal", retryable = false),
            "an internal detail" to MessageTransportResult.HubError("internal", "none", "rpc: connection closed", retryable = false),
            "no code" to MessageTransportResult.HubError(null, "none", "Refused without a code", retryable = false),
            "a wait" to MessageTransportResult.HubError("assistant_offline", "none", "The assistant is restarting", retryable = true),
        )
        for ((index, case) in cases.withIndex()) {
            val (label, answer) = case
            transport.answers += answer
            sender.send(session, label, "case-$index")
            runCurrent()
            val entry = store.entries.value.single { it.clientMessageId == "case-$index" }
            val expected = if (answer.retryable) OutboxState.WaitingForNetwork else OutboxState.Refused
            assertEquals(label, expected, entry.state)
            assertEquals(label, null, entry.reason)
            if (answer.retryable) assertTrue(sender.cancel("case-$index"))
        }
        sender.close()
    }

    @Test fun interruptedAndUnknownRequestsOnlyRetryWithDurableHubDeduplication() = runTest {
        val store = MemoryOutboxStore()
        store.enqueue(OutboxEntry("message-a", session, "hello", state = OutboxState.Sending, createdAt = 100))
        val legacy = FakeTransport(online = true)
        val first = OutboxMessageSender(scope, store, legacy, backgroundScope)
        runCurrent()
        assertEquals(OutboxState.Unknown, store.state("message-a"))
        assertTrue(legacy.requests.isEmpty())
        first.close()

        val modern = FakeTransport(online = true, supportsClientOps = true)
        modern.answers += MessageTransportResult.NoReceipt
        modern.answers += MessageTransportResult.Accepted
        val second = OutboxMessageSender(scope, store, modern, backgroundScope)
        runCurrent()
        assertEquals(OutboxState.Sent, store.state("message-a"))
        assertEquals(2, modern.requests.size)
        modern.requests.forEach {
            assertEquals("message-a", it.headers["X-Persona-Op-Id"])
            assertEquals(scope, it.scope)
        }
        assertTrue(store.transitions.contains(OutboxState.Unknown to OutboxState.Sending))
        second.close()
    }

    @Test fun transcriptCopyWithTheAgentsOwnIdShowsTheMessage() = runTest {
        // Claude Code keeps its own uuid for the user's message: the client message id never comes back (#895).
        val store = MemoryOutboxStore()
        val transport = FakeTransport(online = true, supportsClientOps = true)
        val sentAt = java.time.Instant.parse("2026-11-03T09:00:00Z")
        var clock = sentAt.toEpochMilli()
        val sender = OutboxMessageSender(scope, store, transport, backgroundScope, now = { clock })
        fun user(id: String, text: String, at: java.time.Instant) =
            Item(id, 1, at.toString(), role = "user", kind = "text", text = text)

        transport.answers += MessageTransportResult.Accepted
        sender.send(session, "可以删的都删掉吧", "first")
        runCurrent()
        assertEquals(OutboxState.Sent, store.state("first"))
        // An older message with the same words is not this one.
        sender.observeTranscript(session, listOf(user("old-uuid", "可以删的都删掉吧", sentAt.minusSeconds(3600))))
        runCurrent()
        assertEquals(OutboxState.Sent, store.state("first"))
        // The computer's clock is a little behind and Claude wrapped the text differently.
        sender.observeTranscript(session, listOf(user("claude-uuid", "可以删的都删掉吧\n", sentAt.minusSeconds(30))))
        runCurrent()
        assertEquals(OutboxState.Shown, store.state("first"))

        // Two identical messages: one transcript item shows one of them, seen again it does not show the other.
        clock += 60_000
        transport.answers += MessageTransportResult.Accepted
        transport.answers += MessageTransportResult.Accepted
        sender.send(session, "继续", "again-1")
        sender.send(session, "继续", "again-2")
        runCurrent()
        val again = user("claude-again-1", "继续", sentAt.plusSeconds(61))
        sender.observeTranscript(session, listOf(again))
        sender.observeTranscript(session, listOf(again))
        runCurrent()
        assertEquals(OutboxState.Shown, store.state("again-1"))
        assertEquals(OutboxState.Sent, store.state("again-2"))
        sender.observeTranscript(session, listOf(again, user("claude-again-2", "继续", sentAt.plusSeconds(62))))
        runCurrent()
        assertEquals(OutboxState.Shown, store.state("again-2"))

        // A message still waiting for the network is not taken for the same words typed on the computer.
        transport.readyToSend.value = false
        runCurrent()
        sender.send(session, "看看状态", "offline")
        runCurrent()
        sender.observeTranscript(session, listOf(user("typed-there", "看看状态", sentAt.plusSeconds(70))))
        runCurrent()
        assertEquals(OutboxState.WaitingForNetwork, store.state("offline"))
        sender.close()
    }

    @Test fun anItemTaggedWithTheClientMessageIdShowsThatMessageOnly() = runTest {
        // Hubs since #968 tag every item a sent message became: it decides, whatever the words or the kind.
        val store = MemoryOutboxStore()
        val transport = FakeTransport(online = true, supportsClientOps = true)
        val sentAt = java.time.Instant.parse("2026-11-03T09:00:00Z")
        val sender = OutboxMessageSender(scope, store, transport, backgroundScope, now = { sentAt.toEpochMilli() })
        fun user(id: String, text: String, tag: String?) =
            Item(id, 1, sentAt.plusSeconds(5).toString(), role = "user", kind = "text", text = text, clientMsgId = tag)

        repeat(4) { transport.answers += MessageTransportResult.Accepted }
        sender.send(session, "/compact", "compact")
        sender.send(session, "继续", "again-1")
        sender.send(session, "继续", "again-2")
        sender.send(session, "看看状态", "status")
        runCurrent()
        // A /compact became a notice: no user row ever comes, the tagged notice shows it.
        sender.observeTranscript(session, listOf(
            Item("notice-1", 2, role = "system", kind = "notice", text = "压缩上下文", clientMsgId = "compact")))
        runCurrent()
        assertEquals(OutboxState.Shown, store.state("compact"))

        // The copy tagged for the second "continue" shows that one, not the older one with the same words.
        sender.observeTranscript(session, listOf(user("claude-uuid-2", "继续", "again-2")))
        runCurrent()
        assertEquals(OutboxState.Sent, store.state("again-1"))
        assertEquals(OutboxState.Shown, store.state("again-2"))
        // Other words (the runtime rewrote it), still the tag decides; an image of an already shown message settles nothing.
        sender.observeTranscript(session, listOf(
            Item("image-1", 3, role = "user", kind = "image", clientMsgId = "again-2"),
            user("claude-uuid-1", "[Image #1] 继续下一步", "again-1")))
        runCurrent()
        assertEquals(OutboxState.Shown, store.state("again-1"))
        assertEquals(OutboxState.Sent, store.state("status"))

        // Untagged (an older hub): words still find it.
        sender.observeTranscript(session, listOf(user("claude-uuid-3", "看看状态", null)))
        runCurrent()
        assertEquals(OutboxState.Shown, store.state("status"))
        sender.close()
    }

    @Test fun aDeliveredMessageWhoseCopyNeverShowsGoesOnceItsSessionRests() = runTest {
        // A /compact becomes a notice, Codex writes its queue at the end of the turn: no copy ever comes (#950).
        val store = MemoryOutboxStore()
        val transport = FakeTransport(online = true, supportsClientOps = true)
        val sender = OutboxMessageSender(scope, store, transport, backgroundScope, sentGraceMillis = 60_000)
        var working = true
        sender.connectActivity { it == session && working }
        transport.answers += MessageTransportResult.Accepted
        sender.send(session, "/compact", "compact")
        runCurrent()
        assertEquals(OutboxState.Sent, store.state("compact"))
        advanceTimeBy(59_000)
        runCurrent()
        assertEquals(OutboxState.Sent, store.state("compact"))
        advanceTimeBy(30_000)
        runCurrent()
        assertEquals(OutboxState.Sent, store.state("compact")) // the session still works on it
        working = false
        advanceTimeBy(10_001)
        runCurrent()
        assertEquals(OutboxState.Shown, store.state("compact"))
        sender.close()
    }

    @Test fun messageTheHubDidNotDeliverGoesAgainUnderANewOperationId() = runTest {
        val store = MemoryOutboxStore()
        val hub = ReplayingHub()
        val sender = OutboxMessageSender(scope, store, hub, backgroundScope)
        // The computer is offline for the first try (503, delivered none), back for the next.
        hub.answers += MessageTransportResult.HubError("assistant_offline", "none", retryable = true)
        hub.answers += MessageTransportResult.Accepted
        sender.send(session, "电脑离线时写的", "offline")
        runCurrent()
        hub.readyToSend.value = false
        runCurrent()
        hub.readyToSend.value = true
        runCurrent()
        val ops = hub.requests.map { it.headers.getValue("X-Persona-Op-Id") }
        assertEquals(2, ops.size)
        assertEquals("offline", ops[0])
        assertTrue(ops[1] != ops[0])
        assertEquals(OutboxState.Sent, store.state("offline"))
        sender.observeTranscript(session, listOf(Item("echo", 1, role = "user", kind = "text", text = "电脑离线时写的",
            clientMsgId = ops[1])))
        assertEquals(OutboxState.Shown, store.state("offline"))
        sender.close()
    }

    /** The computer asleep (502 node_unreachable, delivered none): refused at once with its code, and Retry sends it anew. */
    @Test fun aMessageToASleepingComputerIsRefusedAndRetriedByHand() = runTest {
        val store = MemoryOutboxStore()
        val hub = ReplayingHub()
        val sender = OutboxMessageSender(scope, store, hub, backgroundScope)
        hub.answers += MessageTransportResult.HubError("node_unreachable", "none", retryable = true)
        sender.send(session, "电脑睡着时写的", "asleep")
        runCurrent()
        advanceTimeBy(60_000); runCurrent()
        assertEquals(OutboxState.Refused, store.state("asleep"))
        assertEquals("node_unreachable", store.entries.value.single().error)
        assertEquals(1, hub.requests.size)
        hub.answers += MessageTransportResult.Accepted
        assertTrue(sender.retry("asleep"))
        runCurrent()
        val ops = hub.requests.map { it.headers.getValue("X-Persona-Op-Id") }
        assertEquals(2, ops.size)
        assertTrue(ops[1] != ops[0])
        assertEquals(OutboxState.Sent, store.state("asleep"))
        sender.close()
    }

    /** Waiting while the link is up (503 assistant_offline): tried again every 10 s, refused after 60 s (iOS OutboxWait). */
    @Test fun aMessageThatKeepsWaitingWhileTheLinkIsUpGoesAgainThenIsRefused() = runTest {
        val restarting = MessageTransportResult.HubError("assistant_offline", "none", retryable = true)
        data class Case(val label: String, val answers: List<MessageTransportResult>, val after: Long,
            val state: OutboxState, val sends: Int, val error: String?)
        for (c in listOf(
            Case("back within 10 s", listOf(restarting, MessageTransportResult.Accepted), 10_000, OutboxState.Sent, 2, null),
            Case("still restarting after 60 s", List(6) { restarting }, 60_000, OutboxState.Refused, 6, "assistant_offline"),
        )) {
            val store = MemoryOutboxStore()
            val transport = FakeTransport(online = true)
            val sender = OutboxMessageSender(scope, store, transport, backgroundScope)
            runCurrent() // the start's own looks at the queue
            transport.answers += c.answers
            sender.send(session, "助手重启时写的", "restart")
            runCurrent()
            assertEquals(c.label, OutboxState.WaitingForNetwork, store.state("restart"))
            advanceTimeBy(c.after); runCurrent()
            assertEquals(c.label, c.state, store.state("restart"))
            assertEquals(c.label, c.sends, transport.requests.size)
            assertEquals(c.label, c.error, store.entries.value.single().error)
            sender.close()
        }
    }

    /**
     * Retry now on a message still on its way (iOS's Retry Now on a waiting one): the queue looks again at once rather than at
     * its next wake; with the link down it stays waiting. The outbox says when each was written, for where a failed one sits.
     */
    @Test fun retryNowOnAWaitingMessageTriesAtOnce() = runTest {
        val restarting = MessageTransportResult.HubError("assistant_offline", "none", retryable = true)
        val store = MemoryOutboxStore()
        val transport = FakeTransport(online = true)
        val sender = OutboxMessageSender(scope, store, transport, backgroundScope) { 1_700_000_000_000L }
        runCurrent()
        transport.answers += listOf(restarting, MessageTransportResult.Accepted)
        sender.send(session, "written while the assistant restarts", "restart")
        runCurrent()
        assertEquals(OutboxState.WaitingForNetwork, store.state("restart"))
        assertEquals(1_700_000_000_000L, sender.outbox.value.single().createdAt)
        assertTrue(sender.retry("restart"))
        runCurrent()
        assertEquals(OutboxState.Sent, store.state("restart"))
        assertEquals(2, transport.requests.size)

        transport.readyToSend.value = false
        sender.send(session, "written offline", "offline")
        runCurrent()
        assertTrue(sender.retry("offline"))
        runCurrent()
        assertEquals(OutboxState.WaitingForNetwork, store.state("offline"))
        assertEquals(2, transport.requests.size)
        // One already delivered is past retrying.
        assertFalse(sender.retry("restart"))
        assertEquals(OutboxState.Sent, store.state("restart"))
        sender.close()
    }

    /** A main conversation that cannot start gets a minute on a link that is up; the link dropping starts it again. */
    @Test fun aMainConversationThatCannotStartIsRefusedWithTheHubsReason() = runTest {
        val store = MemoryOutboxStore()
        val transport = FakeTransport(online = true)
        val resolver = object : MainConversationResolver {
            override suspend fun findMainConversation(): SessionKey? = null
            override suspend fun createMainConversation(): SessionKey = throw Refusal(503, "assistant_offline")
        }
        val sender = OutboxMessageSender(scope, store, transport, backgroundScope)
        sender.connectMainConversation(resolver)
        sender.sendToMain("你好", "main-a")
        runCurrent()
        advanceTimeBy(50_000); runCurrent()
        transport.readyToSend.value = false; runCurrent(); transport.readyToSend.value = true; runCurrent()
        advanceTimeBy(50_000); runCurrent()
        assertEquals(OutboxState.WaitingForNetwork, store.state("main-a"))
        advanceTimeBy(10_000); runCurrent()
        assertEquals(OutboxState.Refused, store.state("main-a"))
        assertEquals("assistant_offline", store.entries.value.single().error)
        assertTrue(transport.requests.isEmpty())
        sender.close()
    }

    @Test fun refusedMessageRetriedByHandGoesUnderANewOperationId() = runTest {
        val store = MemoryOutboxStore()
        val hub = ReplayingHub()
        val sender = OutboxMessageSender(scope, store, hub, backgroundScope)
        hub.answers += MessageTransportResult.HubError("input_draft", "none")
        sender.send(session, "电脑上有草稿", "draft")
        runCurrent()
        assertEquals(OutboxState.Refused, store.state("draft"))
        hub.answers += MessageTransportResult.Accepted
        assertTrue(sender.retry("draft"))
        runCurrent()
        val ops = hub.requests.map { it.headers.getValue("X-Persona-Op-Id") }
        assertEquals(2, ops.size)
        assertTrue(ops[1] != ops[0])
        assertEquals(OutboxState.Sent, store.state("draft"))
        // Still the same message under its first id for the pages.
        assertEquals("draft", sender.outbox.value.single().clientMessageId)
        sender.close()
    }

    @Test fun retryRefusedRequeuesOnlyExactSessionStateAndCodeInStoredOrder() = runTest {
        val store = MemoryOutboxStore()
        val transport = FakeTransport(online = false)
        val sender = OutboxMessageSender(scope, store, transport, backgroundScope)
        runCurrent()
        val code = "input_draft"
        val otherSession = SessionKey("machine", "other")
        val original = listOf(
            OutboxEntry("queued", session, "queued", state = OutboxState.Queued, error = code, createdAt = 100),
            OutboxEntry("first", session, "first", state = OutboxState.Refused, error = code, createdAt = 101),
            OutboxEntry("sending", session, "sending", state = OutboxState.Sending, error = code, createdAt = 102),
            OutboxEntry("unknown", session, "unknown", state = OutboxState.Unknown, error = code, createdAt = 103),
            OutboxEntry("other-code", session, "other code", state = OutboxState.Refused,
                error = "dialog_open", createdAt = 104),
            OutboxEntry("verifying", session, "verifying", state = OutboxState.Verifying, error = code, createdAt = 105),
            OutboxEntry("other-session", otherSession, "other session", state = OutboxState.Refused,
                error = code, createdAt = 106),
            OutboxEntry("second", session, "second", state = OutboxState.Refused, error = code, createdAt = 107),
            OutboxEntry("needs-confirmation", session, "needs confirmation",
                state = OutboxState.NeedsConfirmation, error = code, createdAt = 108),
            OutboxEntry("without-code", session, "without code", state = OutboxState.Refused, createdAt = 109),
        )
        original.forEach { store.enqueue(it) }

        assertEquals(2, sender.retryRefused(session, code))

        val matches = setOf("first", "second")
        assertEquals(original.map {
            if (it.clientMessageId in matches) it.copyForDelivery(state = OutboxState.Queued, error = null) else it
        }, store.entries.value)
        assertEquals(listOf(
            OutboxState.Refused to OutboxState.Queued,
            OutboxState.Refused to OutboxState.Queued,
        ), store.transitions)
        assertTrue(transport.requests.isEmpty())
        sender.close()
    }

    @Test fun retryRefusedWakesOneDrainThatSendsInPersistedOrder() = runTest {
        val store = MemoryOutboxStore()
        val transport = FakeTransport(online = true)
        val sender = OutboxMessageSender(scope, store, transport, backgroundScope)
        runCurrent()
        val code = "input_draft"
        listOf("first", "other-code", "second", "third").forEachIndexed { index, id ->
            store.enqueue(OutboxEntry(id, session, id, state = OutboxState.Refused,
                error = if (id == "other-code") "dialog_open" else code, createdAt = 100L + index))
        }
        repeat(3) { transport.answers += MessageTransportResult.Accepted }

        assertEquals(3, sender.retryRefused(session, code))
        assertEquals(listOf(OutboxState.Queued, OutboxState.Refused, OutboxState.Queued, OutboxState.Queued),
            store.entries.value.map { it.state })
        assertTrue("retryRefused only wakes the sender", transport.requests.isEmpty())

        runCurrent()
        assertEquals(listOf("first", "second", "third"), transport.requests.map { it.clientMessageId })
        assertEquals(listOf(OutboxState.Sent, OutboxState.Refused, OutboxState.Sent, OutboxState.Sent),
            store.entries.value.map { it.state })

        assertEquals(0, sender.retryRefused(session, code))
        runCurrent()
        assertEquals("settled messages are not sent again", 3, transport.requests.size)
        sender.close()
    }

    @Test fun retryRefusedDoesNotCountOrResendWhenTheConditionalTransitionFails() = runTest {
        val memory = MemoryOutboxStore()
        var raced = false
        val store = object : OutboxStore by memory {
            override suspend fun transition(id: String, from: Set<OutboxState>, to: OutboxState, error: String?): Boolean {
                if (id == "delivered" && from == setOf(OutboxState.Refused) && to == OutboxState.Queued) {
                    // Lose the race after retryRefused's current-state check but before its conditional write.
                    raced = true
                    memory.entries.value = memory.entries.value.map {
                        if (it.clientMessageId == id) it.copyForDelivery(state = OutboxState.Sent) else it
                    }
                }
                return memory.transition(id, from, to, error)
            }
        }
        val transport = FakeTransport(online = true)
        val sender = OutboxMessageSender(scope, store, transport, backgroundScope)
        runCurrent()
        memory.enqueue(OutboxEntry("delivered", session, "delivered", state = OutboxState.Refused,
            error = "input_draft", createdAt = 100))
        memory.enqueue(OutboxEntry("retry", session, "retry", state = OutboxState.Refused,
            error = "input_draft", createdAt = 101))
        transport.answers += MessageTransportResult.Accepted

        assertEquals(1, sender.retryRefused(session, "input_draft"))
        assertTrue(raced)
        assertEquals(OutboxState.Sent, memory.state("delivered"))
        assertEquals(OutboxState.Queued, memory.state("retry"))

        runCurrent()
        assertEquals(listOf("retry"), transport.requests.map { it.clientMessageId })
        assertEquals(OutboxState.Sent, memory.state("delivered"))
        sender.close()
    }

    @Test fun launcherAndModelStayOnEverySendAndRetry() = runTest {
        val store = MemoryOutboxStore()
        val hub = ReplayingHub()
        val sender = OutboxMessageSender(scope, store, hub, backgroundScope)
        hub.answers += MessageTransportResult.Accepted
        hub.answers += MessageTransportResult.Accepted
        sender.send(session, "first", "first", null, "runtime", "model-id")
        sender.send(session, "second", "second", null, "runtime", "model-id")
        runCurrent()
        assertEquals(listOf("first", "second"), hub.requests.map { it.clientMessageId })
        assertTrue(hub.requests.all { it.launcher == "runtime" && it.model == "model-id" })

        hub.answers += MessageTransportResult.HubError("input_draft", "none", retryable = false)
        sender.send(session, "retry", "retry", null, "runtime", "model-id")
        runCurrent()
        assertEquals(OutboxState.Refused, store.state("retry"))
        hub.answers += MessageTransportResult.Accepted
        assertTrue(sender.retry("retry"))
        runCurrent()
        assertEquals("runtime", hub.requests.last().launcher)
        assertEquals("model-id", hub.requests.last().model)
        assertTrue(hub.requests.last().opId != "retry")
        sender.close()
    }

    @Test fun assistantOfflineWithoutDeliveredFieldRotatesOpIdAndRetriesViaStallLoop() = runTest {
        val store = MemoryOutboxStore()
        val hub = ReplayingHub()
        val sender = OutboxMessageSender(scope, store, hub, backgroundScope)
        runCurrent()
        // Legacy hub returns 503 assistant_offline without delivered:"none" and leaves the old opId poisoned as unknown.
        hub.answers += MessageTransportResult.HubError("assistant_offline", delivered = null, error = "assistant offline", retryable = false)
        hub.answers += MessageTransportResult.Accepted
        sender.send(session, "hello during blue-green", "legacy-offline")
        runCurrent()
        assertEquals(OutboxState.WaitingForNetwork, store.state("legacy-offline"))
        assertEquals(1, hub.requests.size)

        advanceTimeBy(10_000)
        runCurrent()
        assertEquals(OutboxState.Sent, store.state("legacy-offline"))
        assertEquals(2, hub.requests.size)
        assertTrue("retry after legacy assistant_offline must rotate opId", hub.requests[1].opId != hub.requests[0].opId)
        sender.close()
    }

    @Test fun retryOnNeedsConfirmationReconcilesFinalizedUnknownAndRotatesOpId() = runTest {
        val store = MemoryOutboxStore()
        val transport = FakeTransport(online = true, supportsClientOps = true)
        val sender = OutboxMessageSender(scope, store, transport, backgroundScope,
            reviewTimeoutMillis = 100, reconciliationTimeoutMillis = 50)
        runCurrent()
        store.enqueue(OutboxEntry("stuck", session, "retry me", state = OutboxState.NeedsConfirmation, createdAt = 10))
        // First retry: hub reports finalized unknown receipt (e.g. RecoverClientOps or 502 unknown) -> rotate opId and resend.
        // Simulate a second failure landing back in NeedsConfirmation, then a second retry succeeding.
        transport.evidence["stuck"] = MessageEvidence.Receipt(MessageTransportResult.NoReceipt)
        transport.answers += MessageTransportResult.NoReceipt
        transport.answers += MessageTransportResult.NoReceipt
        assertTrue(sender.retry("stuck"))
        runCurrent()
        val firstRetryOp = transport.requests.first().opId
        assertTrue("retry after finalized unknown receipt must rotate opId", firstRetryOp != "stuck")
        advanceTimeBy(110)
        runCurrent()
        assertEquals(OutboxState.NeedsConfirmation, store.state("stuck"))

        transport.evidence["stuck"] = MessageEvidence.Receipt(
            MessageTransportResult.HubError("send_failed", "unknown", "uncertain", false))
        transport.answers += MessageTransportResult.Accepted
        assertTrue(sender.retry("stuck"))
        runCurrent()
        assertEquals(OutboxState.Sent, store.state("stuck"))
        val secondRetryOp = transport.requests.last().opId
        assertTrue("second retry after another finalized unknown must rotate opId again", secondRetryOp != firstRetryOp)
        sender.close()
    }

    @Test fun retryOnNeedsConfirmationConvergesWithoutResendingWhenServerAlreadyReceived() = runTest {
        data class Case(val id: String, val evidence: MessageEvidence, val expected: OutboxState)
        val cases = listOf(
            Case("shown", MessageEvidence.Shown, OutboxState.Shown),
            Case("accepted", MessageEvidence.Receipt(MessageTransportResult.Accepted), OutboxState.Sent),
            Case("kept", MessageEvidence.Receipt(MessageTransportResult.Kept), OutboxState.Kept),
            Case("held", MessageEvidence.Receipt(MessageTransportResult.Held), OutboxState.Held),
            Case("delivered-yes", MessageEvidence.Receipt(MessageTransportResult.HubError("internal", "yes", "err", false)), OutboxState.Sent),
        )
        for (c in cases) {
            val store = MemoryOutboxStore()
            val transport = FakeTransport(online = true, supportsClientOps = true)
            val sender = OutboxMessageSender(scope, store, transport, backgroundScope)
            runCurrent()
            store.enqueue(OutboxEntry(c.id, session, "already landed", state = OutboxState.NeedsConfirmation, createdAt = 10))
            transport.evidence[c.id] = c.evidence
            assertTrue(c.id, sender.retry(c.id))
            runCurrent()
            assertEquals(c.id, c.expected, store.state(c.id))
            assertEquals("${c.id} must not resend", 0, transport.requests.size)
            sender.close()
        }
    }

    @Test fun retryOnNeedsConfirmationKeepsOpIdWhenUnresolvedOrOffline() = runTest {
        val store = MemoryOutboxStore()
        val transport = FakeTransport(online = true, supportsClientOps = true)
        val sender = OutboxMessageSender(scope, store, transport, backgroundScope)
        runCurrent()
        store.enqueue(OutboxEntry("pending-op", session, "keep id", state = OutboxState.NeedsConfirmation, createdAt = 10))
        // reconcile returns null (404 / pending): keep the original opId so hub deduplication protects against duplicates.
        transport.answers += MessageTransportResult.Accepted
        assertTrue(sender.retry("pending-op"))
        runCurrent()
        assertEquals(OutboxState.Sent, store.state("pending-op"))
        assertEquals(listOf("pending-op"), transport.requests.map { it.opId })
        sender.close()
    }

    @Test fun confirmNotDeliveredRotatesOpIdBeforeRetry() = runTest {
        val store = MemoryOutboxStore()
        val hub = ReplayingHub()
        val sender = OutboxMessageSender(scope, store, hub, backgroundScope, reviewTimeoutMillis = 100)
        runCurrent()
        hub.answers += MessageTransportResult.NoReceipt
        sender.send(session, "manual confirm", "confirm-msg")
        runCurrent()
        advanceTimeBy(110)
        runCurrent()
        assertEquals(OutboxState.NeedsConfirmation, store.state("confirm-msg"))
        assertTrue(sender.confirm("confirm-msg", delivered = false))
        assertEquals(OutboxState.Refused, store.state("confirm-msg"))
        val rotated = store.entries.value.single().operationId
        assertTrue("confirm(delivered=false) must rotate opId", rotated != "confirm-msg")

        hub.answers += MessageTransportResult.Accepted
        assertTrue(sender.retry("confirm-msg"))
        runCurrent()
        assertEquals(OutboxState.Sent, store.state("confirm-msg"))
        assertEquals(rotated, hub.requests.last().opId)
        sender.close()
    }

    /** Like the hub's client ops: a repeated operation id gets the first answer again (a 503 replays as unknown). */
    private class ReplayingHub : MessageTransport {
        override val readyToSend = MutableStateFlow(true)
        override val supportsClientOps = true
        val answers = ArrayDeque<MessageTransportResult>()
        val requests = mutableListOf<MessageRequest>()
        private val first = mutableMapOf<String, MessageTransportResult>()
        override suspend fun send(request: MessageRequest): MessageTransportResult {
            requests += request
            val op = request.headers.getValue("X-Persona-Op-Id")
            val stored = first[op] ?: return answers.removeFirst().also { first[op] = it }
            return if (stored is MessageTransportResult.HubError && stored.retryable) MessageTransportResult.NoReceipt else stored
        }
    }

    private class FakeTransport(online: Boolean, override val supportsClientOps: Boolean = false) : MessageTransport {
        override val readyToSend = MutableStateFlow(online)
        val answers = ArrayDeque<MessageTransportResult>()
        val requests = mutableListOf<MessageRequest>()
        val lookups = mutableListOf<String>()
        val evidence = mutableMapOf<String, MessageEvidence>()
        var hangLookup = false
        override suspend fun reconcile(request: MessageRequest, createdAt: Long, allowTextMatch: Boolean): MessageEvidence? {
            lookups += request.clientMessageId
            if (hangLookup) awaitCancellation()
            return evidence[request.clientMessageId]
        }
        override suspend fun send(request: MessageRequest): MessageTransportResult {
            requests += request
            return answers.removeFirst()
        }
    }
}
