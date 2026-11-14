package persona.core.domain.tasks

import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.combine
import kotlinx.coroutines.launch
import kotlinx.coroutines.runBlocking
import kotlinx.serialization.json.JsonObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test
import persona.core.contracts.FeedItem
import persona.core.contracts.FeedResponse
import persona.core.contracts.ServerEvent
import persona.core.contracts.TaskPlan
import persona.core.contracts.Todo
import persona.core.contracts.api.ConnectionState
import persona.core.contracts.api.HubConnection
import persona.core.contracts.api.TasksApi

/** A missing to-do is gone only after the list that answers the read is the one on screen (#5110). */
class TodosReadyTest {
    private class Connection : HubConnection {
        override val state = MutableStateFlow(ConnectionState.WaitingForNetwork)
        override val events = MutableSharedFlow<ServerEvent>()
        override fun reconnect() = Unit
        override fun send(message: JsonObject) = true
    }

    private class Api(private val rows: List<Todo>) : TasksApi {
        override suspend fun todos() = rows
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

    @Test fun aFreshStoreIsNotReadyUntilItsHubAnswers() {
        val store = TasksStore(Api(emptyList()), Connection(), CoroutineScope(Dispatchers.Unconfined))
        assertFalse(store.todosReady.value)
    }

    @Test fun aCachedListDoesNotCountAsTheHubsAnswer() = runBlocking {
        val cache = object : TodoCache {
            override suspend fun read() = listOf(Todo("cached", "Cached"))
            override suspend fun write(todos: List<Todo>) = Unit
        }
        val store = TasksStore(Api(emptyList()), Connection(), CoroutineScope(Dispatchers.Unconfined), cache)
        assertEquals(listOf("cached"), store.todos.value.map { it.id })
        assertFalse(store.todosReady.value)
    }

    @Test fun theListArrivesBeforeTheReadCountsAsAnswered() = runBlocking {
        val store = TasksStore(Api(listOf(Todo("t", "Kept"))), Connection(), CoroutineScope(Dispatchers.Unconfined))
        val seen = mutableListOf<Pair<Boolean, List<String>>>()
        val job = launch(Dispatchers.Unconfined) {
            store.todosReady.combine(store.todos) { ready, rows -> ready to rows.map { it.id } }.collect { seen += it }
        }
        store.reloadTodos()
        assertFalse(seen.any { (ready, ids) -> ready && "t" !in ids })
        assertTrue(seen.any { (ready, ids) -> ready && ids == listOf("t") })
        job.cancel()
    }
}
