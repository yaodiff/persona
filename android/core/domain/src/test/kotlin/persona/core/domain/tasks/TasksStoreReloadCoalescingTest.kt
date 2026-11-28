package persona.core.domain.tasks

import java.util.concurrent.atomic.AtomicInteger
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.test.advanceTimeBy
import kotlinx.coroutines.test.runCurrent
import kotlinx.coroutines.test.runTest
import kotlinx.serialization.json.JsonObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import persona.core.contracts.FeedItem
import persona.core.contracts.FeedResponse
import persona.core.contracts.ServerEvent
import persona.core.contracts.TaskPlan
import persona.core.contracts.Todo
import persona.core.contracts.TodosEvent
import persona.core.contracts.api.ConnectionState
import persona.core.contracts.api.HubConnection
import persona.core.contracts.api.TasksApi

@OptIn(ExperimentalCoroutinesApi::class)
class TasksStoreReloadCoalescingTest {
    private class StubConnection : HubConnection {
        override val state = MutableStateFlow(ConnectionState.WaitingForNetwork)
        override val events = MutableSharedFlow<ServerEvent>(extraBufferCapacity = 64)
        override fun reconnect() = Unit
        override fun send(message: JsonObject): Boolean = true
    }

    private class CountingApi : TasksApi {
        val calls = AtomicInteger(0)
        val inFlight = AtomicInteger(0)
        val maxInFlight = AtomicInteger(0)
        @Volatile var gate: CompletableDeferred<Unit>? = null
        @Volatile var firstStarted: CompletableDeferred<Unit>? = null
        @Volatile var response: List<Todo> = emptyList()

        override suspend fun todos(): List<Todo> {
            val active = inFlight.incrementAndGet()
            maxInFlight.accumulateAndGet(active, ::maxOf)
            try {
                val n = calls.incrementAndGet()
                if (n == 1) firstStarted?.complete(Unit)
                gate?.await()
                return response
            } finally {
                inFlight.decrementAndGet()
            }
        }

        override suspend fun feed(conversation: String, since: String?) = FeedResponse()
        override suspend fun startPlan(id: String, remember: Boolean): TaskPlan = error("unused")
        override suspend fun dismissPlan(id: String): TaskPlan = error("unused")
        override suspend fun retryTask(todoId: String): Todo = error("unused")
        override suspend fun removeTask(todoId: String) = Unit
        override suspend fun route(conversation: String, todoId: String, text: String): FeedItem = error("unused")
        override suspend fun today(conversation: String): FeedItem = error("unused")
        override suspend fun mainConversation(): String? = null
        override suspend fun inboxReply(itemId: String, text: String, card: String?) = Unit
    }

    @Test
    fun twentyConcurrentTodosEventsCoalesceToAtMostTwoFetches() = runTest {
        val api = CountingApi()
        val connection = StubConnection()
        val store = TasksStore(api, connection, backgroundScope, todoEventDebounceMillis = 250L)

        // Burst of 20 TodosEvents queued before the leading fetch executes collapses into 1 fetch.
        api.response = listOf(Todo("t1", "First burst"))
        repeat(20) { assertTrue(connection.events.tryEmit(TodosEvent())) }
        runCurrent()
        assertEquals(1, api.calls.get())
        assertEquals(listOf("t1"), store.todos.value.map { it.id })

        // Burst of 20 TodosEvents arriving while a fetch is already in flight coalesces into 1 debounced follow-up.
        api.calls.set(0)
        val gate = CompletableDeferred<Unit>()
        val started = CompletableDeferred<Unit>()
        api.gate = gate
        api.firstStarted = started
        api.response = listOf(Todo("t2", "In-flight"))
        assertTrue(connection.events.tryEmit(TodosEvent()))
        runCurrent()
        started.await()
        assertEquals(1, api.calls.get())

        api.response = listOf(Todo("t3", "Latest after burst"))
        repeat(20) { assertTrue(connection.events.tryEmit(TodosEvent())) }
        runCurrent()
        assertEquals(1, api.calls.get())

        api.gate = null
        gate.complete(Unit)
        runCurrent()
        // Follow-up settles for todoEventDebounceMillis to absorb burst tails before firing once.
        assertEquals(1, api.calls.get())
        advanceTimeBy(249L)
        runCurrent()
        assertEquals(1, api.calls.get())
        advanceTimeBy(1L)
        runCurrent()
        assertTrue("Expected <= 2 fetches for 20 concurrent events, got ${api.calls.get()}", api.calls.get() in 1..2)
        assertEquals(2, api.calls.get())
        assertEquals(1, api.maxInFlight.get())
        assertEquals(listOf("t3"), store.todos.value.map { it.id })
    }

    @Test
    fun directReloadWakesDebouncedFollowUpImmediately() = runTest {
        val api = CountingApi()
        val connection = StubConnection()
        val store = TasksStore(api, connection, backgroundScope, todoEventDebounceMillis = 500L)

        val gate = CompletableDeferred<Unit>()
        val started = CompletableDeferred<Unit>()
        api.gate = gate
        api.firstStarted = started
        api.response = listOf(Todo("first", "First"))
        assertTrue(connection.events.tryEmit(TodosEvent()))
        runCurrent()
        started.await()

        // A TodosEvent during in-flight fetch schedules a 500ms trailing settle window.
        assertTrue(connection.events.tryEmit(TodosEvent()))
        runCurrent()
        api.gate = null
        api.response = listOf(Todo("fast", "Immediate"))
        gate.complete(Unit)
        runCurrent()
        assertEquals(1, api.calls.get())

        // Direct reload (e.g. after a local mutation or reconnect) wakes the pending settle window at t = 0.
        store.reloadTodos()
        runCurrent()
        assertEquals(2, api.calls.get())
        assertEquals(listOf("fast"), store.todos.value.map { it.id })

        advanceTimeBy(1_000L)
        runCurrent()
        assertEquals(2, api.calls.get())
    }
}
