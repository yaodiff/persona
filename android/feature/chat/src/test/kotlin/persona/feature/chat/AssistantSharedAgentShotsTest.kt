package persona.feature.chat

import android.content.res.Configuration
import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.*
import androidx.compose.runtime.snapshots.Snapshot
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.LocalConfiguration
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.*
import kotlinx.coroutines.flow.*
import kotlinx.serialization.json.JsonObject
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.*
import persona.core.contracts.api.*
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import persona.core.domain.tasks.TasksStore
import java.io.File
import java.time.Instant

/** The real composer trays: one shared Agent request, one home and one count (#7531). */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h900dp-xhdpi", application = QuestionTestApplication::class)
class AssistantSharedAgentShotsTest {
    @get:Rule val rule = createComposeRule()
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.Main.immediate)
    @After fun close() = scope.cancel()

    private class Api(var rows: List<Todo>) : TasksApi {
        override suspend fun todos() = rows
        override suspend fun mainConversation() = "main"
        override suspend fun feed(conversation: String, since: String?) = FeedResponse(emptyList())
        override suspend fun today(conversation: String) = FeedItem("today", conversation, kind = "today")
        override suspend fun startPlan(id: String, remember: Boolean): TaskPlan = error("unused")
        override suspend fun dismissPlan(id: String): TaskPlan = error("unused")
        override suspend fun retryTask(todoId: String): Todo = error("unused")
        override suspend fun removeTask(todoId: String) = Unit
        override suspend fun route(conversation: String, todoId: String, text: String): FeedItem = error("unused")
        override suspend fun inboxReply(itemId: String, text: String, card: String?) = Unit
    }

    private fun settle() {
        Snapshot.sendApplyNotifications()
        rule.mainClock.advanceTimeBy(300)
        rule.waitForIdle()
    }

    private fun badge(entry: String, count: Int) {
        rule.onNodeWithTag("assistant-entry-$entry").assertTextContains(count.toString())
    }

    private fun shot(name: String) {
        val image = rule.onNodeWithTag("shared-agent-trays").captureToImage().asAndroidBitmap()
        val directory = File("build/assistant-shared-agent-shots").also { it.mkdirs() }
        File(directory, "$name.png").outputStream().use { image.compress(Bitmap.CompressFormat.PNG, 100, it) }
    }

    @Test fun oneHomeAndMatchingBadgesInEveryStyleAndTheme() {
        val at = Instant.now().toString()
        val shared = Session("box", "shared", title = "Network tests", state = "needs_you", status = "blocked",
            updatedAt = at, todo = SessionTodo("release", "Release the app"))
        val agent = TodoAgent("box", "shared", linkedAt = at)
        val todos = listOf("cache" to "Choose a cache", "release" to "Release the app", "tests" to "Add network tests").map { (id, title) ->
            Todo(id, title, origin = "main", agents = listOf(agent), createdAt = at, updatedAt = at)
        }
        val request = Interaction("permission", machineId = "box", sessionId = "shared", kind = "permission",
            toolName = "Bash", summary = "Run network tests", createdAt = at)
        val connection = object : HubConnection {
            override val state = MutableStateFlow(ConnectionState.WaitingForNetwork)
            override val events = MutableSharedFlow<ServerEvent>()
            override fun reconnect() = Unit
            override fun send(message: JsonObject) = true
        }
        val api = Api(todos)
        val tasks = TasksStore(api, connection, scope).also { it.reloadTodos(); it.loadTodayIfMain("main") }
        val tray = AssistantTrayState()
        val approved = mutableListOf<String>()
        var sessions by mutableStateOf(listOf(shared))
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        rule.mainClock.autoAdvance = false
        rule.setContent {
            val configuration = Configuration(LocalConfiguration.current).apply {
                uiMode = (uiMode and Configuration.UI_MODE_NIGHT_MASK.inv()) or
                    if (dark) Configuration.UI_MODE_NIGHT_YES else Configuration.UI_MODE_NIGHT_NO
            }
            CompositionLocalProvider(LocalConfiguration provides configuration) {
                PersonaTheme(style = style, darkTheme = dark, reduceMotion = true) {
                    Column(Modifier.width(400.dp).background(PersonaTheme.palette.background).testTag("shared-agent-trays")) {
                        val host = AssistantTaskHost(tasks, "main", sessions, { it }, {}, { throw it }, {}, pending = listOf(request))
                        AssistantTrays(host, tray, listOf(request), emptyList(), emptyList(), 400.dp, busy = false, enabled = true,
                            onApprove = { batch -> approved += batch.map { it.first.id } }, onReview = {}, onSend = {}, onSchedule = {})
                    }
                }
            }
        }
        settle()
        for (s in AppStyle.entries) for (d in listOf(false, true)) {
            style = s; dark = d; tray.expanded = null; settle()
            badge("needs", 1); badge("running", 2)
            rule.onNodeWithTag("assistant-entry-needs").performClick(); settle()
            rule.onNodeWithTag("assistant-needs-task-release").assertIsDisplayed()
            rule.onNodeWithTag("assistant-needs-task-cache").assertDoesNotExist()
            rule.onNodeWithTag("assistant-needs-task-tests").assertDoesNotExist()
            rule.onAllNodesWithTag("assistant-decision-permission").assertCountEquals(1)
            val suffix = "${s.name.lowercase()}-${if (d) "dark" else "light"}"
            shot("needs-$suffix")
            rule.onNodeWithTag("assistant-entry-running").performClick(); settle()
            for (id in listOf("cache", "tests")) {
                val row = hasAnyAncestor(hasTestTag("assistant-progress-todo:$id"))
                rule.onNodeWithTag("assistant-progress-todo:$id").assertIsDisplayed()
                rule.onNode(hasText("In progress") and row, useUnmergedTree = true).assertIsDisplayed()
                rule.onNode(hasTestTag("assistant-work-spinner") and row, useUnmergedTree = true).assertIsDisplayed()
            }
            rule.onNodeWithTag("assistant-progress-todo:release").assertDoesNotExist()
            rule.onNodeWithText("Blocked").assertDoesNotExist()
            rule.onNodeWithTag("assistant-agent-shared").assertDoesNotExist()
            shot("running-$suffix")
        }
        // No hub tie: the first current lister holds the same single request and count.
        sessions = listOf(shared.copy(todo = null)); tray.expanded = AssistantTray.NeedsYou; settle()
        badge("needs", 1); badge("running", 2)
        rule.onNodeWithTag("assistant-needs-task-cache").assertIsDisplayed()
        rule.onNodeWithTag("assistant-needs-task-release").assertDoesNotExist()
        // Its own review still needs the owner, with no duplicate Agent approval.
        sessions = listOf(shared)
        api.rows = todos.map { if (it.id == "tests") it.copy(supervision = Supervision(state = "pending_review")) else it }
        tasks.reloadTodos(); settle()
        badge("needs", 2); badge("running", 1)
        rule.onNodeWithTag("assistant-review-todo:tests").assertIsDisplayed()
        rule.onAllNodesWithTag("assistant-decision-permission").assertCountEquals(1)
        rule.onNodeWithTag("assistant-approve-all").performClick(); settle()
        assertEquals(listOf(request.id), approved)
        // An idle Agent of its own makes the other task Stopped, rather than blocked by the shared request.
        val idle = Session("box", "idle", state = "idle", status = "idle")
        sessions = listOf(shared, idle)
        api.rows = todos.map { if (it.id == "tests") it.copy(agents = it.agents + TodoAgent("box", "idle")) else it }
        tasks.reloadTodos(); tray.expanded = AssistantTray.Running; settle()
        badge("needs", 1); badge("running", 2)
        val stopped = hasAnyAncestor(hasTestTag("assistant-progress-todo:tests"))
        rule.onNode(hasText("Stopped") and stopped, useUnmergedTree = true).assertIsDisplayed()
        rule.onNode(hasTestTag("assistant-work-spinner") and stopped, useUnmergedTree = true).assertDoesNotExist()
    }

    @Test fun anUnsharedRunningTaskKeepsItsProjectedBlockedAndStoppedAppearance() {
        val at = Instant.now().toString()
        val idle = Session("box", "solo", title = "Independent task", state = "idle", status = "idle")
        val todo = Todo("solo", idle.title, origin = "main", agents = listOf(TodoAgent("box", "solo")), createdAt = at, updatedAt = at)
        // An ask without a verdict is not an open decision, but the existing projection still draws it as blocked.
        val blocked = todo.copy(ask = TodoAsk(askedAt = at))
        val api = Api(listOf(blocked))
        val connection = object : HubConnection {
            override val state = MutableStateFlow(ConnectionState.WaitingForNetwork)
            override val events = MutableSharedFlow<ServerEvent>()
            override fun reconnect() = Unit
            override fun send(message: JsonObject) = true
        }
        val tasks = TasksStore(api, connection, scope).also { it.reloadTodos(); it.loadTodayIfMain("main") }
        val host = AssistantTaskHost(tasks, "main", listOf(idle), { it }, {}, { throw it }, {})
        val tray = AssistantTrayState().also { it.expanded = AssistantTray.Running }
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        rule.mainClock.autoAdvance = false
        rule.setContent {
            val configuration = Configuration(LocalConfiguration.current).apply {
                uiMode = (uiMode and Configuration.UI_MODE_NIGHT_MASK.inv()) or
                    if (dark) Configuration.UI_MODE_NIGHT_YES else Configuration.UI_MODE_NIGHT_NO
            }
            CompositionLocalProvider(LocalConfiguration provides configuration) {
                PersonaTheme(style = style, darkTheme = dark, reduceMotion = true) {
                    Column(Modifier.width(400.dp).background(PersonaTheme.palette.background).testTag("shared-agent-trays")) {
                        AssistantTrays(host, tray, emptyList(), emptyList(), emptyList(), 400.dp, busy = false, enabled = true,
                            onApprove = {}, onReview = {}, onSend = {}, onSchedule = {})
                    }
                }
            }
        }
        for (s in AppStyle.entries) for (d in listOf(false, true)) {
            style = s; dark = d; api.rows = listOf(blocked); tasks.reloadTodos(); settle()
            val projection = trayProgress(api.rows, emptyList(), host).single()
            assertTrue(projection.blocked); assertFalse(projection.stopped)
            badge("running", 1)
            val row = hasAnyAncestor(hasTestTag("assistant-progress-todo:solo"))
            rule.onNode(hasText("Blocked") and row, useUnmergedTree = true).assertIsDisplayed()
            rule.onNode(hasText("Stopped") and row, useUnmergedTree = true).assertDoesNotExist()
            val suffix = "${s.name.lowercase()}-${if (d) "dark" else "light"}"
            shot("unshared-blocked-$suffix")
            api.rows = listOf(todo); tasks.reloadTodos(); settle()
            val stopped = trayProgress(api.rows, emptyList(), host).single()
            assertFalse(stopped.blocked); assertTrue(stopped.stopped)
            rule.onNode(hasText("Stopped") and row, useUnmergedTree = true).assertIsDisplayed()
            rule.onNode(hasText("Blocked") and row, useUnmergedTree = true).assertDoesNotExist()
            shot("unshared-stopped-$suffix")
        }
    }
}
