package persona.core.domain.tasks

import java.time.Clock
import java.time.Instant
import java.time.ZoneId
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.launch
import kotlinx.coroutines.runBlocking
import kotlinx.coroutines.yield
import kotlinx.serialization.json.JsonObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test
import persona.core.contracts.FeedItem
import persona.core.contracts.FeedResponse
import persona.core.contracts.LnModeResponse
import persona.core.contracts.PushIntercept
import persona.core.contracts.PushInterceptsResponse
import persona.core.contracts.ServerEvent
import persona.core.contracts.TaskPlan
import persona.core.contracts.Todo
import persona.core.contracts.api.ConnectionState
import persona.core.contracts.api.HubConnection
import persona.core.contracts.api.TasksApi

class TasksStorePushInterceptsTest {
    private val now = Instant.parse("2026-11-14T04:00:00Z")

    private class Api(var enabled: Boolean, var rows: List<PushIntercept>) : TasksApi {
        var modeCalls = 0
        var listCalls = 0
        var listError: Exception? = null
        var gate: CompletableDeferred<Unit>? = null
        override suspend fun lnMode(): LnModeResponse { modeCalls++; return LnModeResponse(enabled) }
        override suspend fun pushIntercepts(): PushInterceptsResponse {
            listCalls++
            gate?.await()
            listError?.let { throw it }
            return PushInterceptsResponse(rows)
        }
        override suspend fun todos() = emptyList<Todo>()
        override suspend fun feed(conversation: String, since: String?) = FeedResponse(emptyList())
        override suspend fun startPlan(id: String, remember: Boolean): TaskPlan = error("unused")
        override suspend fun dismissPlan(id: String): TaskPlan = error("unused")
        override suspend fun retryTask(todoId: String): Todo = error("unused")
        override suspend fun removeTask(todoId: String) = Unit
        override suspend fun route(conversation: String, todoId: String, text: String): FeedItem = error("unused")
        override suspend fun today(conversation: String): FeedItem = error("unused")
        override suspend fun mainConversation(): String? = null
        override suspend fun inboxReply(itemId: String, text: String, card: String?) = Unit
    }

    private class Connection : HubConnection {
        override val state = MutableStateFlow(ConnectionState.WaitingForNetwork)
        override val events = MutableSharedFlow<ServerEvent>()
        override fun reconnect() = Unit
        override fun send(message: JsonObject): Boolean = true
    }

    private fun row(at: String, title: String) = PushIntercept(at, "P1", "ln_p1_held", title = title)

    @Test fun offModeDoesNotRequestIntercepts() = runBlocking {
        val api = Api(false, emptyList())
        val scope = CoroutineScope(SupervisorJob() + Dispatchers.Unconfined)
        val store = TasksStore(api, Connection(), scope, clock = Clock.fixed(now, ZoneId.of("Asia/Shanghai")))
        store.refreshPushIntercepts()
        assertFalse(store.lnModeEnabled.value)
        assertEquals(1, api.modeCalls)
        assertEquals(0, api.listCalls)
        scope.cancel()
    }

    @Test fun enabledModeReadsRowsAndListFailureKeepsRetry() = runBlocking {
        val api = Api(true, listOf(row("2026-11-14T03:00:00Z", "One")))
        val scope = CoroutineScope(SupervisorJob() + Dispatchers.Unconfined)
        val store = TasksStore(api, Connection(), scope, clock = Clock.fixed(now, ZoneId.of("Asia/Shanghai")))
        store.refreshPushIntercepts()
        assertTrue(store.lnModeEnabled.value)
        assertEquals(1, store.heldPushes.value.total)
        assertEquals(1, api.listCalls)

        api.listError = RuntimeException("404")
        store.refreshPushIntercepts()
        assertFalse(store.lnModeEnabled.value)
        assertTrue(store.heldPushesFailed.value)
        scope.cancel()
    }

    @Test fun lateResponseCannotOverwriteNewerRead() = runBlocking {
        val api = Api(true, listOf(row("2026-11-14T02:00:00Z", "Old")))
        api.gate = CompletableDeferred()
        val scope = CoroutineScope(SupervisorJob() + Dispatchers.Unconfined)
        val store = TasksStore(api, Connection(), scope, clock = Clock.fixed(now, ZoneId.of("Asia/Shanghai")))
        val first = scope.launch { store.refreshPushIntercepts() }
        yield()
        val gate = api.gate
        api.gate = null
        api.rows = listOf(row("2026-11-14T03:00:00Z", "New"))
        store.refreshPushIntercepts()
        gate?.complete(Unit)
        first.join()
        assertEquals("New", store.heldPushes.value.p1.single().summaries.single())
        scope.cancel()
    }

    @Test fun offlineTransitionClearsLoadingWhenAStaleReadIsCancelled() = runBlocking {
        val api = Api(true, listOf(row("2026-11-14T02:00:00Z", "Old")))
        api.gate = CompletableDeferred()
        val connection = Connection()
        val scope = CoroutineScope(SupervisorJob() + Dispatchers.Unconfined)
        val store = TasksStore(api, connection, scope, clock = Clock.fixed(now, ZoneId.of("Asia/Shanghai")))
        val first = scope.launch { store.refreshPushIntercepts() }
        yield()
        assertTrue(store.heldPushesLoading.value)
        connection.state.value = ConnectionState.Connecting
        assertFalse(store.heldPushesLoading.value)
        connection.state.value = ConnectionState.WaitingForNetwork
        assertFalse(store.heldPushesLoading.value)
        api.gate?.complete(Unit)
        first.join()
        assertFalse(store.heldPushesLoading.value)
        scope.cancel()
    }
}
