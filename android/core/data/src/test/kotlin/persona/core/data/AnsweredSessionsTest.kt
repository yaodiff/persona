package persona.core.data

import persona.core.contracts.*
import persona.core.contracts.api.*
import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.SharingStarted
import kotlinx.coroutines.flow.stateIn
import kotlinx.coroutines.test.TestScope
import kotlinx.coroutines.test.advanceTimeBy
import kotlinx.coroutines.test.runCurrent
import kotlinx.coroutines.test.runTest
import kotlinx.coroutines.delay
import kotlinx.serialization.json.JsonObject
import org.junit.Assert.*
import org.junit.Test
import java.time.Instant

/** A card answered on the phone stops its agent's rows saying "Needs you" before the hub's next sessions push (#4525). */
@OptIn(ExperimentalCoroutinesApi::class)
class AnsweredSessionsTest {
    private val waiting = Session("mac", "a", status = "blocked", state = "needs_you", stateSince = "2026-11-06T08:00:00Z",
        pendingInteractions = 1)
    private val other = Session("mac", "b", status = "blocked", state = "needs_you", stateSince = "2026-11-06T08:00:00Z",
        pendingInteractions = 1)
    private val card = Interaction("ix1", "mac", "a", kind = "permission")
    private val otherCard = Interaction("ix2", "mac", "b", kind = "permission")

    private class Fixture(scope: TestScope, sessions: List<Session>, pending: List<Interaction>) {
        val connection = Connection()
        val transport = Transport(sessions, pending)
        val repository = HubSessionsRepository(transport, connection, scope.backgroundScope)
        // Cards are answered a second after the agents' needs_you started.
        val center = HubInteractionCenter(transport, connection, scope.backgroundScope, sessions = repository.sessions,
            clock = { Instant.parse("2026-11-06T08:00:01Z") })
        val shown = answeredSessions(repository.sessions, center).stateIn(scope.backgroundScope, SharingStarted.Eagerly, emptyList())
        fun state(id: String) = shown.value.first { it.sessionId == id }.agentState
    }

    private suspend fun TestScope.fixture(sessions: List<Session> = listOf(waiting, other)): Fixture =
        Fixture(this, sessions, listOf(card, otherCard)).also { it.repository.refresh(); it.center.refresh(); runCurrent() }

    @Test fun anAnsweredAgentIsAtWorkUntilTheHubSaysOtherwise() = runTest {
        val f = fixture()
        assertEquals(AgentState.NeedsYou, f.state("a"))

        f.center.resolve(card.id, Resolution(allow = true))
        runCurrent()
        val answered = f.shown.value.first { it.sessionId == "a" }
        assertEquals(AgentState.Working, answered.agentState)
        // The chat's status line reads the host status and the count; both say it goes on.
        assertEquals("working", answered.status)
        assertEquals(0, answered.pendingInteractions)
        // Another agent still waiting on its own card is left alone.
        assertEquals(AgentState.NeedsYou, f.state("b"))

        // The hub's push right after the answer, while the node still says blocked: the same needs_you.
        f.connection.events.emit(SessionsEvent("mac", listOf(waiting, other)))
        runCurrent()
        assertEquals(AgentState.Working, f.state("a"))

        // A new card for it: waiting again at once.
        f.connection.events.emit(InteractionEvent(Interaction("ix3", "mac", "a", kind = "question")))
        runCurrent()
        assertEquals(AgentState.NeedsYou, f.state("a"))
    }

    @Test fun aNeedsYouThatStartedAfterTheAnswerIsANewOne() = runTest {
        val f = fixture()
        f.center.resolve(card.id, Resolution(allow = true))
        runCurrent()
        assertEquals(AgentState.Working, f.state("a"))
        // The hub took the answer (it withdraws the card); the agent went on and stopped again at a dialog without a
        // card (a TUI prompt): the hub's state counts.
        f.connection.events.emit(InteractionResolvedEvent(card.id))
        f.connection.events.emit(SessionsEvent("mac", listOf(waiting.copy(stateSince = "2026-11-06T08:00:05Z"), other)))
        runCurrent()
        assertEquals(AgentState.NeedsYou, f.state("a"))
    }

    @Test fun aNeedsYouPushedBeforeTheCardIsWithdrawnIsTheOneTheAnswerEnded() = runTest {
        val f = fixture()
        f.center.resolve(card.id, Resolution(allow = true))
        runCurrent()
        // A push the hub sent before it took the answer, whatever its time: the same wait (#709).
        f.connection.events.emit(SessionsEvent("mac", listOf(waiting.copy(stateSince = "2026-11-06T08:00:05Z"), other)))
        runCurrent()
        assertEquals(AgentState.Working, f.state("a"))
        // Withdrawn: from now on the hub's needs_you after the answer is a new wait.
        f.connection.events.emit(InteractionResolvedEvent(card.id))
        runCurrent()
        assertEquals(AgentState.NeedsYou, f.state("a"))
        // A new card makes it wait at once while a card is pending too.
        f.center.resolve(otherCard.id, Resolution(allow = true))
        runCurrent()
        assertEquals(AgentState.Working, f.state("b"))
        f.connection.events.emit(InteractionEvent(Interaction("ix3", "mac", "b", kind = "question")))
        runCurrent()
        assertEquals(AgentState.NeedsYou, f.state("b"))
    }

    @Test fun aNeedsYouFromBeforeTheAnswerIsTheOneItEnded() = runTest {
        // Answered from a notification before this device's list had the agent's needs_you: the push that brings it
        // after the hub withdrew the card started before the answer, so it is the one the answer ended (#709).
        val f = fixture(listOf(waiting.copy(status = "working", state = "working", stateSince = "2026-11-06T07:59:00Z",
            pendingInteractions = 0), other))
        f.center.resolve(card.id, Resolution(allow = true))
        f.connection.events.emit(InteractionResolvedEvent(card.id))
        f.connection.events.emit(SessionsEvent("mac", listOf(waiting, other)))
        runCurrent()
        assertEquals(AgentState.Working, f.state("a"))
        // One that started after the answer is a new wait.
        f.connection.events.emit(SessionsEvent("mac", listOf(waiting.copy(stateSince = "2026-11-06T08:00:05Z"), other)))
        runCurrent()
        assertEquals(AgentState.NeedsYou, f.state("a"))
    }

    @Test fun aCardWithdrawnBeforeTheAnswersReplyIsNotPending() = runTest {
        val f = fixture()
        // The hub's interaction_resolved often comes before the answer's own reply.
        f.transport.answering = { f.connection.events.emit(InteractionResolvedEvent(card.id)); delay(10) }
        f.center.resolve(card.id, Resolution(allow = true))
        runCurrent()
        assertEquals(AgentState.Working, f.state("a"))
        f.connection.events.emit(SessionsEvent("mac", listOf(waiting.copy(stateSince = "2026-11-06T08:00:05Z"), other)))
        runCurrent()
        assertEquals(AgentState.NeedsYou, f.state("a"))
    }

    @Test fun afterTheWindowTheHubsStateCountsAgain() = runTest {
        val f = fixture()
        f.center.resolve(card.id, Resolution(allow = true))
        runCurrent()
        assertEquals(AgentState.Working, f.state("a"))
        advanceTimeBy(HubInteractionCenter.ANSWERED_MILLIS + 1)
        runCurrent()
        assertEquals(AgentState.NeedsYou, f.state("a"))
        assertTrue(f.center.answered.value.isEmpty())
    }

    @Test fun aCardThatWasGoneSaysNothingOfItsAgent() = runTest {
        val f = fixture()
        f.transport.gone = true
        assertTrue(runCatching { f.center.resolve(card.id, Resolution(allow = true)) }.exceptionOrNull() is InteractionGoneException)
        runCurrent()
        assertTrue(f.center.answered.value.isEmpty())
        assertEquals(AgentState.NeedsYou, f.state("a"))
    }

    @Test fun aCardTheHubSaysWasAlreadyAnsweredCountsAsAnswered() = runTest {
        val f = fixture()
        f.transport.outcome = ResolveOutcome(already = true, status = "resolved", resolution = Resolution(allow = true))
        f.center.resolveOutcome(card.id, Resolution(allow = false))
        runCurrent()
        assertEquals(AgentState.Working, f.state("a"))
    }

    @Test fun aSecondHandBackIsNotAnAnswer() = runTest {
        val f = fixture()
        // "Hand to the computer" tapped twice: the hub answers the second with already, handed back (#709). The agent
        // still waits on the computer.
        f.transport.outcome = ResolveOutcome(already = true, status = "dismissed", resolution = Resolution(dismiss = true))
        f.center.resolveOutcome(card.id, Resolution(dismiss = true))
        runCurrent()
        assertTrue(f.center.answered.value.isEmpty())
        assertEquals(AgentState.NeedsYou, f.state("a"))
    }

    @Test fun aCardHandedBackToTheComputerLeavesItsAgentWaiting() = runTest {
        val f = fixture()
        // "Hand to the computer": the agent now waits on the computer, so its rows keep saying "Needs you".
        f.center.resolve(card.id, Resolution(dismiss = true))
        runCurrent()
        assertTrue(f.center.answered.value.isEmpty())
        assertEquals(AgentState.NeedsYou, f.state("a"))
        // Handing back a card the hub says was already answered: that answer took effect, its agent goes on.
        f.transport.outcome = ResolveOutcome(already = true, status = "resolved", resolution = Resolution(allow = true))
        f.center.resolveOutcome(otherCard.id, Resolution(dismiss = true))
        runCurrent()
        assertEquals(AgentState.Working, f.state("b"))
    }

    @Test fun aLateAnswerResponseKeepsASameIdReplacement() = runTest {
        val f = fixture()
        val first = card.copy(createdAt = "2026-11-06T08:00:00.000000001Z")
        val replacement = card.copy(createdAt = "2026-11-06T08:00:00.000000002Z", summary = "replacement")
        f.transport.pending = listOf(first)
        f.center.refresh()
        f.transport.answering = {
            f.connection.events.emit(InteractionEvent(replacement))
            delay(10)
        }
        f.center.resolve(first.id, Resolution(allow = true))
        runCurrent()
        assertEquals(listOf(replacement), f.center.pending.value)
        f.transport.pending = listOf(replacement)
        f.center.refresh()
        assertEquals(listOf(replacement), f.center.pending.value)
    }

    @Test fun anEarlierResolvedEventKeepsASameIdReplacement() = runTest {
        val f = fixture()
        val replacement = card.copy(createdAt = "2026-11-06T08:00:00.000000002Z", summary = "replacement")
        f.connection.events.emit(InteractionEvent(replacement))
        val event = ServerEvent.decode("""{"type":"interaction_resolved","id":"ix1","created_at":"2026-11-06T08:00:00.000000001Z"}""")
        assertNotNull(event)
        f.connection.events.emit(event!!)
        runCurrent()
        assertEquals(listOf(replacement), f.center.pending.value.filter { it.id == card.id })
        f.transport.pending = listOf(replacement)
        f.center.refresh()
        assertEquals(listOf(replacement), f.center.pending.value.filter { it.id == card.id })
    }

    @Test fun aNewerLiveReplacementRetainsTheTerminalFenceAgainstAnOlderReplay() = runTest {
        val f = fixture()
        val first = card.copy(createdAt = "2026-11-06T08:00:00.000000001Z", summary = "first")
        val replacement = card.copy(createdAt = "2026-11-06T08:00:00.000000002Z", summary = "replacement")
        f.connection.events.emit(ServerEvent.decode("""{"type":"interaction_resolved","id":"ix1","created_at":"2026-11-06T08:00:00.000000001Z"}""")!!)
        f.connection.events.emit(InteractionEvent(replacement))
        f.connection.events.emit(InteractionEvent(first))
        runCurrent()
        assertEquals(listOf(replacement), f.center.pending.value.filter { it.id == card.id })
    }

    @Test fun anOlderLiveEventCannotReplaceAPendingReplacement() = runTest {
        val f = fixture()
        val first = card.copy(createdAt = "2026-11-06T08:00:00.000000001Z", summary = "first")
        val replacement = card.copy(createdAt = "2026-11-06T08:00:00.000000002Z", summary = "replacement")
        f.connection.events.emit(InteractionEvent(replacement))
        f.connection.events.emit(InteractionEvent(first))
        runCurrent()
        assertEquals(listOf(replacement), f.center.pending.value.filter { it.id == card.id })
        f.transport.pending = listOf(first)
        f.center.refresh()
        assertEquals(listOf(replacement), f.center.pending.value.filter { it.id == card.id })
    }

    @Test fun anOlderLiveEventCannotResurrectAResolvedReplacement() = runTest {
        val f = fixture()
        val first = card.copy(createdAt = "2026-11-06T08:00:00.000000001Z", summary = "first")
        val replacement = card.copy(createdAt = "2026-11-06T08:00:00.000000002Z", summary = "replacement")
        f.connection.events.emit(InteractionEvent(replacement))
        f.connection.events.emit(ServerEvent.decode("""{"type":"interaction_resolved","id":"ix1","created_at":"2026-11-06T08:00:00.000000002Z"}""")!!)
        f.connection.events.emit(InteractionEvent(first))
        runCurrent()
        assertEquals(emptyList<Interaction>(), f.center.pending.value.filter { it.id == card.id })
        f.transport.pending = listOf(first)
        f.center.refresh()
        assertEquals(emptyList<Interaction>(), f.center.pending.value.filter { it.id == card.id })
    }

    @Test fun theRepositoryViewShowsTheAnsweredListAndRefreshesTheHubs() = runTest {
        val f = fixture()
        val view = f.repository.showing(f.shown)
        f.center.resolve(card.id, Resolution(allow = true))
        runCurrent()
        assertEquals(AgentState.Working, view.sessions.value.first { it.sessionId == "a" }.agentState)
        // The hub's own list is untouched, and refreshing goes to the hub's repository.
        assertEquals(AgentState.NeedsYou, f.repository.sessions.value.first { it.sessionId == "a" }.agentState)
        f.transport.list = listOf(waiting.copy(status = "working", state = "working", pendingInteractions = 0), other)
        view.refresh()
        runCurrent()
        assertEquals("working", f.repository.sessions.value.first { it.sessionId == "a" }.status)
        assertSame(f.repository.machines, view.machines)
    }

    @Test fun nothingAnsweredGivesTheSameList() {
        val list = listOf(waiting, other)
        assertSame(list, answeredSessions(list, listOf(card), emptyMap()))
        // Marked, but its card still waits: nothing to change either.
        assertSame(list, answeredSessions(list, listOf(card, otherCard), mapOf(waiting.key to AnsweredMark(waiting.stateSince))))
    }

    /** Offline, so nothing refreshes unless the test asks. */
    private class Connection : HubConnection {
        override val state = MutableStateFlow(ConnectionState.WaitingForNetwork)
        override val events = MutableSharedFlow<ServerEvent>()
        override fun reconnect() = Unit
        override fun send(message: JsonObject): Boolean = true
    }

    private class Transport(var list: List<Session>, var pending: List<Interaction>) : ChatTransport {
        var gone = false
        var outcome = ResolveOutcome()
        /** Runs while an answer is on its way, before the hub replies. */
        var answering: suspend () -> Unit = {}
        override suspend fun machines() = listOf(Machine("mac"))
        override suspend fun sessions() = list
        override suspend fun items(session: SessionKey, query: ItemsQuery): ItemsResponse = error("Unexpected items request")
        override suspend fun interactions() = pending
        override suspend fun resolve(id: String, resolution: Resolution) { if (gone) throw InteractionGoneException("expired") }
        override suspend fun resolveOutcome(id: String, resolution: Resolution): ResolveOutcome {
            resolve(id, resolution); answering(); return outcome
        }
        override suspend fun image(session: SessionKey, itemId: String) = byteArrayOf()
        override suspend fun fullItem(session: SessionKey, itemId: String) = Item(itemId)
    }
}
