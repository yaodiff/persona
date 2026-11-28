package persona.core.data

import persona.core.contracts.*
import persona.core.contracts.api.*
import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.launch
import kotlinx.coroutines.test.UnconfinedTestDispatcher
import kotlinx.coroutines.test.runCurrent
import kotlinx.coroutines.test.runTest
import org.junit.Assert.assertEquals
import org.junit.Test
import kotlinx.serialization.json.JsonObject

/** A request the hub pushes that no card showed yet is an arrival, which the app plays attention for (#4209, iOS `Delegated.announces`). */
@OptIn(ExperimentalCoroutinesApi::class)
class ArrivingRequestsTest {
    private val card = Interaction("ix1", "mac", "a", kind = "permission")

    @Test fun onlyANewCardPushedLiveArrives() = runTest {
        val connection = Connection()
        val center = HubInteractionCenter(Transport(listOf(card)), connection, backgroundScope)
        val arrived = mutableListOf<String>()
        backgroundScope.launch(UnconfinedTestDispatcher(testScheduler)) { center.arrivals.collect { arrived += it.id } }
        // A snapshot that lists a card is no arrival.
        center.refresh()
        runCurrent()
        assertEquals(listOf("ix1"), center.pending.value.map { it.id })
        // The card it showed changing is none either; a new one is.
        connection.events.emit(InteractionEvent(card.copy(kind = "question")))
        connection.events.emit(InteractionEvent(Interaction("ix2", "mac", "a", kind = "question")))
        runCurrent()
        assertEquals(listOf("ix2"), arrived)
        assertEquals(listOf("ix1", "ix2"), center.pending.value.map { it.id })
    }

    @Test fun aPreparedCardsDeadlineUpdateReplacesItsSnapshotWithoutAnotherArrival() = runTest {
        fun prepared(deadline: String) = JsonCodec.decodeFromString<Interaction>(
            """{"id":"ix1","machine_id":"mac","session_id":"a","kind":"permission",""" +
                """"expires_at":"$deadline","can_clarify":true}""",
        )
        val original = prepared("2030-01-01T00:00:00Z")
        val updated = prepared("2030-01-02T00:00:00Z")
        val connection = Connection()
        val center = HubInteractionCenter(Transport(listOf(original)), connection, backgroundScope)
        val arrived = mutableListOf<String>()
        backgroundScope.launch(UnconfinedTestDispatcher(testScheduler)) { center.arrivals.collect { arrived += it.id } }
        center.refresh()
        connection.events.emit(InteractionEvent(updated))
        runCurrent()
        assertEquals(listOf(updated), center.pending.value)
        assertEquals(emptyList<String>(), arrived)
        connection.events.emit(InteractionResolvedEvent(updated.id))
        runCurrent()
        assertEquals(emptyList<Interaction>(), center.pending.value)
        center.refresh()
        assertEquals(emptyList<Interaction>(), center.pending.value)
    }

    @Test fun aDisplayCopyStaysInConversationsWithoutASecondArrival() = runTest {
        val original = Interaction("original", "mac", "side", kind = "permission")
        val mirror = JsonCodec.decodeFromString<Interaction>(
            """{"id":"display","machine_id":"mac","session_id":"main","kind":"permission","mirror_of":"original"}""",
        )
        val connection = Connection()
        val center = HubInteractionCenter(Transport(emptyList()), connection, backgroundScope)
        val arrived = mutableListOf<String>()
        backgroundScope.launch(UnconfinedTestDispatcher(testScheduler)) { center.arrivals.collect { arrived += it.id } }
        connection.events.emit(InteractionEvent(original))
        connection.events.emit(InteractionEvent(mirror))
        runCurrent()
        assertEquals(listOf("original", "display"), center.pending.value.map { it.id })
        assertEquals(listOf("original"), arrived)
        assertEquals("original", mirror.mirrorOf)
    }

    @Test fun aDelegatingCardIsSilentUntilItsHandoverReplacesItById() = runTest {
        fun card(delegating: Boolean, delegate: String? = null) = JsonCodec.decodeFromString<Interaction>(
            """{"id":"ix1","machine_id":"mac","session_id":"a","kind":"question","delegating":$delegating,""" +
                """"questions":[{"question":"Which driver?","options":[{"label":"pgx"},{"label":"sqlc"}]}]""" +
                (delegate?.let { ""","delegate":$it""" } ?: "") + "}",
        )
        val looking = card(true)
        val handed = card(false, """{"what":"Choose the database driver","recommend":"pgx"}""")
        val connection = Connection()
        val center = HubInteractionCenter(Transport(emptyList()), connection, backgroundScope)
        val arrived = mutableListOf<String>()
        backgroundScope.launch(UnconfinedTestDispatcher(testScheduler)) { center.arrivals.collect { arrived += it.id } }

        connection.events.emit(InteractionEvent(looking))
        runCurrent()
        assertEquals(listOf(looking), center.pending.value)
        // This flow drives the arrival sound, haptic and notification: looking is not news yet.
        assertEquals(emptyList<String>(), arrived)

        connection.events.emit(InteractionEvent(handed))
        runCurrent()
        assertEquals(listOf(handed), center.pending.value)
        assertEquals(listOf("ix1"), arrived)

        // Repeating or editing the handed-over copy is not another arrival.
        connection.events.emit(InteractionEvent(handed))
        runCurrent()
        assertEquals(listOf(handed), center.pending.value)
        assertEquals(listOf("ix1"), arrived)
    }

    @Test fun aSameIDReplacementArrivesOnceWithoutAnnouncingItsReplay() = runTest {
        val first = card.copy(createdAt = "2026-11-06T08:00:00.000000001Z")
        val replacement = card.copy(createdAt = "2026-11-06T08:00:00.000000002Z")
        val connection = Connection()
        val center = HubInteractionCenter(Transport(emptyList()), connection, backgroundScope)
        val arrived = mutableListOf<String?>()
        backgroundScope.launch(UnconfinedTestDispatcher(testScheduler)) { center.arrivals.collect { arrived += it.createdAt } }
        connection.events.emit(InteractionEvent(first))
        connection.events.emit(InteractionEvent(first))
        connection.events.emit(InteractionEvent(replacement))
        connection.events.emit(InteractionEvent(replacement))
        runCurrent()
        assertEquals(listOf(first.createdAt, replacement.createdAt), arrived)
        assertEquals(listOf(replacement), center.pending.value)
    }

    private class Connection : HubConnection {
        override val state = MutableStateFlow(ConnectionState.WaitingForNetwork)
        override val events = MutableSharedFlow<ServerEvent>()
        override fun reconnect() = Unit
        override fun send(message: JsonObject): Boolean = true
    }

    private class Transport(val pending: List<Interaction>) : ChatTransport {
        override suspend fun machines() = listOf(Machine("mac"))
        override suspend fun sessions() = emptyList<Session>()
        override suspend fun items(session: SessionKey, query: ItemsQuery): ItemsResponse = error("Unexpected items request")
        override suspend fun interactions() = pending
        override suspend fun resolve(id: String, resolution: Resolution) = Unit
        override suspend fun image(session: SessionKey, itemId: String) = byteArrayOf()
        override suspend fun fullItem(session: SessionKey, itemId: String) = Item(itemId)
    }
}
