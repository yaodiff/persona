package persona.feature.chat

import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.material3.LocalRippleConfiguration
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.runtime.snapshots.Snapshot
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.assertCountEquals
import androidx.compose.ui.test.assertHasNoClickAction
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithTag
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import java.io.File
import java.time.Instant
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.serialization.json.JsonObject
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.FeedItem
import persona.core.contracts.FeedResponse
import persona.core.contracts.ServerEvent
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.contracts.TaskPlan
import persona.core.contracts.Todo
import persona.core.contracts.api.ConnectionState
import persona.core.contracts.api.HubConnection
import persona.core.contracts.api.TasksApi
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.Tokens
import persona.core.domain.tasks.AgentHandoff
import persona.core.domain.tasks.HandoffWake
import persona.core.domain.tasks.TasksStore

/**
 * Hand-off cards in the assistant chat (#5110): one card per Agent, in Glass, Brief and Board, light and dark.
 * Native JVM captures (build/handoff-shots/), not emulator screenshots.
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h1200dp-xhdpi")
class HandoffShotsTest {
    @get:Rule val rule = createComposeRule()
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.Unconfined)
    @After fun close() = scope.cancel()

    private val now = Instant.now()
    private val working = Session("desk", "0f3c2a91-5d7e-4b8a-9c11-2e4f6a8b7ed0", title = "Example Agent", state = "working",
        activityAt = now.minusSeconds(20).toString())
    private val waiting = Session("desk", "needs-you", title = "Waiting Agent", state = "needs_you",
        activityAt = now.minusSeconds(5 * 60).toString())
    private val handoffs = listOf(
        AgentHandoff("desk/${working.sessionId}", "Example task", HandoffWake.Created, "desk", working.sessionId, todoId = "task"),
        AgentHandoff("desk/${waiting.sessionId}", "Waiting task", HandoffWake.Active, "desk", waiting.sessionId, todoId = "wait"),
        AgentHandoff("desk/again", "Resumed task", HandoffWake.Resumed, "desk", "again", agentTitle = "Resumed Agent", todoId = "again"),
        AgentHandoff("todo:starting", "Starting task", HandoffWake.Created, todoId = "starting"),
        AgentHandoff("plan/failed", "Broken task", HandoffWake.Created, todoId = "broken", failure = "launcher missing"),
        AgentHandoff("plan/gone", "Removed task", HandoffWake.Created, ended = true),
    )

    private class Api : TasksApi {
        override suspend fun todos() = emptyList<Todo>()
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
    private class Connection : HubConnection {
        override val state = MutableStateFlow(ConnectionState.WaitingForNetwork)
        override val events = MutableSharedFlow<ServerEvent>()
        override fun reconnect() = Unit
        override fun send(message: JsonObject) = true
    }

    private var opened: SessionKey? = null
    private val sheet = mutableStateOf<TaskSheet?>(null)

    private fun show() {
        val store = TasksStore(Api(), Connection(), scope)
        val host = AssistantTaskHost(store, "main", listOf(working, waiting), { it }, { opened = it }, { throw it }, {}, sheet = sheet)
        rule.setContent {
            PersonaTheme(style = style, darkTheme = dark, reduceMotion = true) {
                CompositionLocalProvider(LocalRippleConfiguration provides null) {
                    Column(Modifier.testTag("shot").width(400.dp).background(PersonaTheme.palette.background).padding(Tokens.Space.l),
                        verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
                        AgentHandoffCards(handoffs, host)
                    }
                }
            }
        }
        rule.waitForIdle()
    }

    @Test fun aCardOpensTheAgentAndKeepsTheSessionIdOutOfSight() {
        show()
        rule.onAllNodesWithTag("handoff-card").assertCountEquals(6)
        rule.onAllNodesWithText("NEW").assertCountEquals(4)
        rule.onNodeWithText("ALREADY ACTIVE").assertIsDisplayed()
        rule.onNodeWithText("RESUMED").assertIsDisplayed()
        rule.onNodeWithText("Example Agent").assertIsDisplayed()
        rule.onNodeWithText("last active just now", substring = true).assertIsDisplayed()
        rule.onNodeWithText("Needs you", substring = true).assertIsDisplayed()
        rule.onNodeWithText("Starting the agent").assertIsDisplayed()
        rule.onNodeWithText("Didn't start: launcher missing").assertIsDisplayed()
        rule.onNodeWithText("This task no longer exists").assertIsDisplayed()
        rule.onNodeWithText("0f3c2a91", substring = true).assertDoesNotExist()
        rule.onNodeWithText("Dispatched:", substring = true).assertDoesNotExist()

        rule.onNodeWithText("Example Agent").performClick()
        rule.runOnIdle { assertEquals(SessionKey("desk", working.sessionId), opened); assertNull(sheet.value); opened = null }
        rule.onNodeWithText("Starting the agent").performClick()
        rule.runOnIdle { assertEquals(TaskSheet.Task("starting"), sheet.value); assertNull(opened) }
        rule.onNodeWithText("This task no longer exists").assertHasNoClickAction()
    }

    @Test fun shotsInEveryStyleLightAndDark() {
        show()
        val output = File("build/handoff-shots").also { it.mkdirs() }
        val light = mutableMapOf<AppStyle, Bitmap>()
        for (s in listOf(AppStyle.Glass, AppStyle.Brief, AppStyle.Board)) for (d in listOf(false, true)) {
            setLook(s, d)
            rule.waitForIdle()
            rule.onNodeWithText("Example Agent").assertIsDisplayed()
            val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
            if (!d) light[s] = bitmap.copy(Bitmap.Config.ARGB_8888, false)
            else assertFalse("Light and dark must differ for $s", light.getValue(s).sameAs(bitmap))
            File(output, "handoff-${s.name.lowercase()}-${if (d) "dark" else "light"}.png").outputStream()
                .use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
        }
    }

    private var style by mutableStateOf(AppStyle.Glass)
    private var dark by mutableStateOf(false)
    private fun setLook(style: AppStyle, dark: Boolean) {
        this.style = style; this.dark = dark
        Snapshot.sendApplyNotifications()
        rule.waitForIdle()
    }
}
