package persona.feature.agents

import androidx.datastore.core.DataStore
import androidx.datastore.preferences.core.Preferences
import androidx.datastore.preferences.core.emptyPreferences
import androidx.lifecycle.viewModelScope
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.cancel
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.launch
import kotlinx.coroutines.test.*
import kotlinx.serialization.json.JsonObject
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Before
import org.junit.Test
import persona.core.contracts.*
import persona.core.contracts.api.*
import persona.core.data.HubInteractionCenter
import persona.core.domain.answer.AnswerAction

/**
 * An answer whose result is unknown on the Agents page is checked against the hub, and the card is not left
 * showing "Unconfirmed" with its buttons disabled (#3791).
 */
@OptIn(ExperimentalCoroutinesApi::class)
class AgentsUnconfirmedAnswerTest {
    private lateinit var model: AgentsViewModel
    private val request = Interaction("request", machineId = "m", sessionId = "s", kind = "permission", toolName = "Bash")
    private val state = MutableStateFlow(ConnectionState.Online)
    private val pending = MutableStateFlow(listOf(request))
    private var resolves = 0
    private var resolve: suspend () -> Unit = { error("acknowledgement lost") }
    private var refresh: suspend () -> Unit = {}
    private var refreshes = 0
    private val preferences = AgentsPreferences(object : DataStore<Preferences> {
        override val data = MutableStateFlow<Preferences>(emptyPreferences())
        override suspend fun updateData(transform: suspend (Preferences) -> Preferences): Preferences =
            transform(data.value).also { data.value = it }
    }, object : HubPreferences {
        override val preferences = MutableStateFlow(HubPreferencesSnapshot())
        override suspend fun refresh() = Unit
        override suspend fun togglePin(path: String, space: Boolean) = Unit
        override suspend fun toggleMute(path: String, space: Boolean) = Unit
        override suspend fun markSeen(session: SessionKey, version: String) = Unit
    })

    @Before fun setMain() { Dispatchers.setMain(StandardTestDispatcher()) }
    @After fun tearDown() {
        if (::model.isInitialized) model.viewModelScope.cancel()
        Dispatchers.resetMain()
    }

    private fun TestScope.start(center: InteractionCenter? = null) {
        model = AgentsViewModel(object : SessionsRepository {
            override val sessions = MutableStateFlow(listOf(Session("m", "s", state = "needs_input")))
            override val machines = MutableStateFlow(listOf(Machine("m", online = true)))
            override suspend fun refresh() = Unit
        }, center ?: object : InteractionCenter {
            override val pending = this@AgentsUnconfirmedAnswerTest.pending
            override suspend fun refresh() { refreshes++; this@AgentsUnconfirmedAnswerTest.refresh() }
            override suspend fun resolve(id: String, resolution: Resolution) { resolves++; this@AgentsUnconfirmedAnswerTest.resolve() }
        }, object : HubConnection {
            override val state = this@AgentsUnconfirmedAnswerTest.state
            override val events = MutableSharedFlow<ServerEvent>()
            override fun reconnect() = Unit
            override fun send(message: JsonObject) = true
        }, object : MessageSender {
            override val outbox = MutableStateFlow<List<OutboxMessage>>(emptyList())
            override suspend fun send(session: SessionKey, text: String, clientMessageId: String, clearDraft: String?) = Unit
        }, preferences)
        backgroundScope.launch(UnconfinedTestDispatcher(testScheduler)) { model.page.collect {} }
        runCurrent()
    }

    @Test fun aCardStillPendingAfterTheCheckCanBeAnsweredAgain() = runTest {
        start()
        model.answer(request, AnswerAction.Allow)
        runCurrent()
        assertEquals(1, refreshes)
        assertEquals(RequestDelivery.Retry, model.delivery.value[request.id])
        assertEquals(false, RequestDeliveries.locks(model.delivery.value[request.id]))

        resolve = {}
        model.answer(request, AnswerAction.Allow)
        runCurrent()
        assertEquals("The second tap is sent", 2, resolves)
        assertEquals(RequestDelivery.Sent, model.delivery.value[request.id])
    }

    @Test fun aCardGoneAfterTheCheckEndsAndSaysSo() = runTest {
        refresh = { pending.value = emptyList() }
        start()
        model.answer(request, AnswerAction.Allow)
        runCurrent()
        assertNull("No 「未确认」 is left behind", model.delivery.value[request.id])
        assertEquals(AgentText(R.string.agents_request_checked_gone), model.notice.value)
    }

    @Test fun aFailedCheckLetsTheCardBeAnsweredAgain() = runTest {
        refresh = { error("hub restarting") }
        start()
        model.answer(request, AnswerAction.Allow)
        runCurrent()
        assertEquals(RequestDelivery.Retry, model.delivery.value[request.id])
    }

    @Test fun aHubThatStaysAwayFreesTheButtonsAfterTheTimeout() = runTest {
        resolve = { state.value = ConnectionState.Connecting; error("hub gone") }
        start()
        model.answer(request, AnswerAction.Allow)
        runCurrent()
        assertEquals("Waits for the hub before it decides", RequestDelivery.Unknown, model.delivery.value[request.id])
        advanceTimeBy(RequestDeliveries.CHECK_TIMEOUT_MILLIS + 1)
        runCurrent()
        assertEquals(0, refreshes)
        assertEquals(RequestDelivery.Retry, model.delivery.value[request.id])
    }

    @Test fun anEntryWhoseCardLeftIsDropped() = runTest {
        resolve = {}
        start()
        model.answer(request, AnswerAction.Allow)
        runCurrent()
        assertEquals(RequestDelivery.Sent, model.delivery.value[request.id])
        pending.value = emptyList()
        runCurrent()
        assertNull(model.delivery.value[request.id])
    }

    @Test fun aTapAnswersTheDisplayedOccurrenceWhenAReplacementArrivesBeforeItsCoroutine() = runTest {
        val first = request.copy(createdAt = "2026-01-02T03:04:05.000000001Z")
        val replacement = request.copy(createdAt = "2026-01-02T03:04:05.000000002Z", summary = "replacement")
        val events = MutableSharedFlow<ServerEvent>(extraBufferCapacity = 4)
        val answered = mutableListOf<Interaction>()
        val transport = object : ChatTransport {
            override suspend fun machines(): List<Machine> = error("unused")
            override suspend fun sessions(): List<Session> = error("unused")
            override suspend fun items(session: SessionKey, query: ItemsQuery): ItemsResponse = error("unused")
            override suspend fun interactions(): List<Interaction> = error("unused")
            override suspend fun resolve(id: String, resolution: Resolution) = error("unused")
            override suspend fun resolveOccurrence(interaction: Interaction, resolution: Resolution, handoffCard: Boolean): ResolveOutcome {
                answered += interaction
                return ResolveOutcome()
            }
            override suspend fun image(session: SessionKey, itemId: String): ByteArray = error("unused")
            override suspend fun fullItem(session: SessionKey, itemId: String): Item = error("unused")
        }
        val centerScope = CoroutineScope(UnconfinedTestDispatcher(testScheduler))
        try {
            val center = HubInteractionCenter(transport, object : HubConnection {
                override val state = this@AgentsUnconfirmedAnswerTest.state
                override val events = events
                override val resumed = true
                override fun reconnect() = Unit
                override fun send(message: JsonObject) = true
            }, centerScope)
            events.emit(InteractionEvent(first))
            start(center)
            model.answer(first, AnswerAction.Allow)
            // The tap passed the page's current-card check, but its coroutine has not reached the center yet.
            events.emit(InteractionEvent(replacement))
            runCurrent()
            assertEquals("The displayed A must not turn into an approval of its replacement B", listOf(first), answered)
        } finally {
            centerScope.cancel()
        }
    }
}
