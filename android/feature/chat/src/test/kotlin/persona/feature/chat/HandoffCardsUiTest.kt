package persona.feature.chat

import androidx.compose.foundation.layout.Column
import androidx.compose.ui.test.assertCountEquals
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithText
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.FeedItem
import persona.core.contracts.FeedResponse
import persona.core.contracts.PlanTask
import persona.core.contracts.ServerEvent
import persona.core.contracts.TaskPlan
import persona.core.contracts.Todo
import persona.core.contracts.api.ConnectionState
import persona.core.contracts.api.HubConnection
import persona.core.contracts.api.TasksApi
import persona.core.designsystem.PersonaTheme
import persona.core.domain.lines.ChatLine
import persona.core.domain.tasks.AssistantTaskCards
import persona.core.domain.tasks.DispatchHint
import persona.core.domain.tasks.TasksStore

/**
 * The cards the conversation draws, through [DispatchHandoffCards]: a to-do still on its way is starting, a plan's
 * task stays New, and a failed receipt below the card takes the reason off it (#5110).
 */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "en")
class HandoffCardsUiTest {
    @get:Rule val rule = createComposeRule()

    private class Connection : HubConnection {
        override val state = MutableStateFlow(ConnectionState.WaitingForNetwork)
        override val events = MutableSharedFlow<ServerEvent>()
        override fun reconnect() = Unit
        override fun send(message: JsonObject) = true
    }

    private class Api : TasksApi {
        var rows = emptyList<Todo>()
        val gate = CompletableDeferred<Unit>()
        var items = emptyList<FeedItem>()
        override suspend fun todos(): List<Todo> { gate.await(); return rows }
        override suspend fun feed(conversation: String, since: String?) = FeedResponse(items)
        override suspend fun startPlan(id: String, remember: Boolean): TaskPlan = error("unused")
        override suspend fun dismissPlan(id: String): TaskPlan = error("unused")
        override suspend fun retryTask(todoId: String): Todo = error("unused")
        override suspend fun removeTask(todoId: String) = Unit
        override suspend fun route(conversation: String, todoId: String, text: String): FeedItem = error("unused")
        override suspend fun today(conversation: String): FeedItem = error("unused")
        override suspend fun mainConversation(): String? = null
        override suspend fun inboxReply(itemId: String, text: String, card: String?) = Unit
    }

    private fun hint(lines: List<ChatLine>) =
        lines.filterIsInstance<ChatLine.Extra>().mapNotNull { it.payload as? DispatchHint }.single()

    @Test fun theCardsFollowTheStoreNotAPrebuiltList() {
        val at = "2026-11-12T10:00:00Z"
        val plan = TaskPlan("p", "main", listOf(PlanTask("Planned work", todoId = "planned")), "started", at)
        val active = FeedItem("task-planned", "main", at, "task", "planned", buildJsonObject { put("wake", "active") })
        val planned = hint(AssistantTaskCards.lines(emptyList(),
            listOf(FeedItem("plan", "main", at, "plan", "p"), active), mapOf("p" to plan),
            listOf(Todo("planned", "Planned work", origin = "main", planId = "p"))))
        val resumed = FeedItem("task-alone", "main", at, "task", "alone", buildJsonObject { put("wake", "resumed") })
        val alone = hint(AssistantTaskCards.lines(emptyList(), listOf(resumed), emptyMap(),
            listOf(Todo("alone", "Already running", origin = "main"))))
        val broken = hint(AssistantTaskCards.lines(emptyList(),
            listOf(FeedItem("task-broken", "main", at, "task", "broken")), emptyMap(),
            listOf(Todo("broken", "Broken task", origin = "main", startError = "launcher missing"))))
        val api = Api()
        val scope = CoroutineScope(Dispatchers.Unconfined)
        val store = TasksStore(api, Connection(), scope)
        store.reloadTodos()
        val host = AssistantTaskHost(store, "main", emptyList(), { it }, {}, { throw it }, {})
        rule.setContent {
            PersonaTheme {
                Column {
                    DispatchHandoffCards(planned, host)
                    DispatchHandoffCards(alone, host)
                    DispatchHandoffCards(broken, host)
                }
            }
        }
        rule.onAllNodesWithText("Starting the agent").assertCountEquals(3)
        rule.onNodeWithText("This task no longer exists").assertDoesNotExist()
        rule.onAllNodesWithText("NEW").assertCountEquals(2)
        rule.onNodeWithText("RESUMED").assertIsDisplayed()

        api.rows = listOf(
            Todo("planned", "Planned work", origin = "main", planId = "p"),
            Todo("alone", "Already running", origin = "main"),
            Todo("broken", "Broken task", origin = "main", startError = "launcher missing"),
        )
        api.items = listOf(FeedItem("receipt", "main", at, "receipt", "broken", buildJsonObject { put("state", "failed") }))
        api.gate.complete(Unit)
        store.load("main")
        rule.waitForIdle()

        rule.onNodeWithText("This task no longer exists").assertDoesNotExist()
        rule.onNodeWithText("launcher missing").assertDoesNotExist()
        rule.onNodeWithText("Didn't start", substring = false).assertIsDisplayed()
        rule.onAllNodesWithText("Starting the agent").assertCountEquals(2)
        rule.onAllNodesWithText("NEW").assertCountEquals(2)
        rule.onNodeWithText("RESUMED").assertIsDisplayed()
        rule.onNodeWithText("ALREADY ACTIVE").assertDoesNotExist()
    }
}
