package persona.android.di

import java.io.File
import java.lang.reflect.Proxy
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.async
import kotlinx.coroutines.cancel
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.runBlocking
import kotlinx.serialization.json.JsonObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertSame
import org.junit.Assert.assertTrue
import org.junit.Test
import persona.android.ui.resolveHubStack
import persona.core.contracts.FeedItem
import persona.core.contracts.FeedResponse
import persona.core.contracts.HubScope
import persona.core.contracts.ServerEvent
import persona.core.contracts.SessionKey
import persona.core.contracts.TaskPlan
import persona.core.contracts.Todo
import persona.core.contracts.api.ChatComposerService
import persona.core.contracts.api.ComposerDelivery
import persona.core.contracts.api.ConnectionState
import persona.core.contracts.api.DraftSnapshot
import persona.core.contracts.api.HubConnection
import persona.core.contracts.api.MainConversationSender
import persona.core.contracts.api.OutboxMessage
import persona.core.contracts.api.PhoneQuickResults
import persona.core.contracts.api.TasksApi
import persona.core.data.ScopedHubData
import persona.core.domain.tasks.QuickResult
import persona.core.domain.tasks.TasksStore
import persona.feature.chat.ChatAppBindings

/** Verifies each hub owns a single shared TasksStore reused by both chat bindings and AppServices. */
class SharedTasksStoreWiringTest {
    private class StubConnection : HubConnection {
        override val state = MutableStateFlow(ConnectionState.WaitingForNetwork)
        override val events = MutableSharedFlow<ServerEvent>()
        override fun reconnect() = Unit
        override fun send(message: JsonObject): Boolean = true
    }

    private class StubTasksApi : TasksApi {
        override suspend fun todos(): List<Todo> = emptyList()
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

    private fun bindingsWith(scope: CoroutineScope, tasks: TasksStore): ChatAppBindings {
        val composer = object : ChatComposerService {
            override val deliveries = MutableStateFlow<Map<String, ComposerDelivery>>(emptyMap())
            override suspend fun upload(session: SessionKey, name: String, file: File) = error("unused")
            override suspend fun interrupt(session: SessionKey) = error("unused")
            override suspend fun retryRefused(clientMessageId: String, clearDraft: Boolean) = error("unused")
        }
        val sender = object : MainConversationSender {
            override val outbox = MutableStateFlow<List<OutboxMessage>>(emptyList())
            override suspend fun sendToMain(text: String, clientMessageId: String, draft: DraftSnapshot?) = error("unused")
        }
        return ChatAppBindings(
            HubScope("hub-a", "gen-1"),
            scope,
            unused(),
            StubConnection(),
            unused(),
            composer,
            unused(),
            unused(),
            sender,
            tasks = tasks,
        )
    }

    @Test
    fun groupStackAndAppServicesResolveSingleSharedTasksStoreAndPreserveQuickResults() = runBlocking {
        val scope = CoroutineScope(SupervisorJob() + Dispatchers.Unconfined)
        try {
            var removedId: Long? = null
            val quickResults = PhoneQuickResults { id -> removedId = id }
            val storeA = TasksStore(StubTasksApi(), StubConnection(), scope, quickResults = quickResults)
            val storeB = TasksStore(StubTasksApi(), StubConnection(), scope)

            val plainA = bindingsWith(scope, storeA)
            val groupedA = plainA.inGroup(GroupNames("g0a1b2c3d"))
            val plainB = bindingsWith(scope, storeB)
            assertSame(storeA, plainA.tasks)
            assertSame(storeA, groupedA.tasks)

            val dataA = allocateStub<ScopedHubData>()
            val dataB = allocateStub<ScopedHubData>()
            val groupA = allocateStub<GroupRuntime>()
            val groupB = allocateStub<GroupRuntime>()
            val stackA = GroupStack(groupA, dataA, allocateStub(), allocateStub(), groupedA, plainA)
            val stackB = GroupStack(groupB, dataB, allocateStub(), allocateStub(), plainB, plainB)

            // AppServices resolves the matching stack even when GroupStacks finishes building asynchronously.
            val stacksFlow = MutableStateFlow<List<GroupStack>>(emptyList())
            val resolvedA = async(Dispatchers.Unconfined) { resolveHubStack(stacksFlow, dataA) }
            stacksFlow.value = listOf(stackA, stackB)
            val matchedA = resolvedA.await()
            val matchedB = resolveHubStack(stacksFlow, dataB)

            assertSame(storeA, matchedA.tasks)
            assertSame(matchedA.bindings.tasks, matchedA.tasks)
            assertSame(storeB, matchedB.tasks)

            // Shared TasksStore preserves quickResults undo for calendar cards.
            val shared = requireNotNull(matchedA.tasks)
            val card = QuickResult(
                reminder = false,
                callId = "call-1",
                itemId = 42L,
                title = "Dentist",
                time = "10:00",
                place = null,
            )
            assertTrue(shared.canUndoQuickResult(card))
            assertTrue(shared.undoQuickResult(card))
            assertEquals(42L, removedId)
        } finally {
            scope.cancel()
        }
    }

    private inline fun <reified T> unused(): T = Proxy.newProxyInstance(T::class.java.classLoader, arrayOf(T::class.java)) { proxy, method, args ->
        when (method.name) {
            "toString" -> "Unused"
            "hashCode" -> System.identityHashCode(proxy)
            "equals" -> proxy === args?.firstOrNull()
            else -> error("Unexpected access: ${method.name}")
        }
    } as T

    @Suppress("UNCHECKED_CAST")
    private inline fun <reified T> allocateStub(): T {
        val unsafeClass = Class.forName("sun.misc.Unsafe")
        val field = unsafeClass.getDeclaredField("theUnsafe").apply { isAccessible = true }
        val unsafe = field.get(null)
        return unsafeClass.getMethod("allocateInstance", Class::class.java).invoke(unsafe, T::class.java) as T
    }
}
