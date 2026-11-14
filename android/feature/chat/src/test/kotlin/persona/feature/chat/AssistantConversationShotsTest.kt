package persona.feature.chat

import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.*
import androidx.compose.runtime.*
import androidx.compose.runtime.snapshots.Snapshot
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import coil3.ImageLoader
import kotlinx.coroutines.*
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.serialization.json.*
import org.junit.Assert.*
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.MessageSource
import persona.core.contracts.*
import persona.core.contracts.api.*
import persona.core.designsystem.*
import persona.core.domain.lines.ChatLine
import persona.core.domain.tasks.*
import java.io.File

/** Real message/feed renderers, with synthetic data. These are Native JVM captures, not emulator screenshots. */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "zh-w400dp-h1200dp-xhdpi")
class AssistantConversationShotsTest {
    @get:Rule val rule = createComposeRule()
    private val session = SessionKey("computer", "main")
    private val plan = TaskPlan("plan", "main", listOf(PlanTask("Tidy the conversation", todoId = "task")), "started", at(8))
    private val todo = Todo("task", "Tidy the conversation", origin = "main", planId = plan.id, createdAt = at(8), quiet = true,
        agents = listOf(TodoAgent("computer", "agent", title = "Layout agent")))
    private val transcript = listOf(
        Item("compact", 1, ts = at(1), role = "system", kind = "notice", text = "Context compacted"),
        Item("background", 2, ts = at(2), role = "user", kind = "text", text = "Inspect background work",
            source = MessageSource("supervision", title = "Background check")),
        Item("quiet-answer", 3, ts = at(3), role = "assistant", kind = "text", text = "No decision needed"),
        Item("question", 4, ts = at(4), role = "user", kind = "text", text = "Simplify the Assistant conversation"),
        Item("read", 5, ts = at(5), role = "assistant", kind = "tool_use", toolUseId = "read", toolName = "Read",
            toolInput = buildJsonObject { put("file_path", "/sample/chat.kt") }),
        Item("read-result", 6, ts = at(6), role = "user", kind = "tool_result", toolUseId = "read", text = "Source evidence"),
        Item("search", 7, ts = at(7), role = "assistant", kind = "tool_use", toolUseId = "search", toolName = "Grep",
            toolInput = buildJsonObject { put("pattern", "layout") }),
        Item("search-result", 8, ts = at(8), role = "user", kind = "tool_result", toolUseId = "search", text = "Two matches"),
        Item("answer", 9, ts = at(9), role = "assistant", kind = "text", text = "Keep the final answer here. Progress stays in its panel."),
    )
    private val feed = listOf(
        FeedItem("plan-feed", "main", at(8), "plan", "plan"),
        FeedItem("task-feed", "main", at(10), "task", "task"),
        FeedItem("receipt-feed", "main", at(11), "receipt", "task", buildJsonObject {
            put("title", "Assistant layout"); put("summary", "Verification passed. Evidence remains available.")
        }),
    )
    private class Api(var todo: Todo, val plan: TaskPlan) : TasksApi {
        override suspend fun todos() = listOf(todo)
        override suspend fun feed(conversation: String, since: String?) = FeedResponse(plans = mapOf(plan.id to plan))
        override suspend fun startPlan(id: String, remember: Boolean) = plan
        override suspend fun dismissPlan(id: String) = plan.copy(state = "dismissed")
        override suspend fun retryTask(todoId: String) = todo
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
    private fun at(n: Int) = "2026-11-10T10:00:${n.toString().padStart(2, '0')}Z"

    private fun verify(style: AppStyle, language: String = "zh") {
        val scope = CoroutineScope(SupervisorJob() + Dispatchers.Unconfined)
        val api = Api(todo, plan)
        val store = TasksStore(api, Connection(), scope)
        store.reloadTodos(); store.load("main")
        var simplified by mutableStateOf(false)
        var dark by mutableStateOf(false)
        var expanded by mutableStateOf(emptySet<String>())
        // a tap on the hand-off card opens that Agent's page (#5110)
        val sheet = mutableStateOf<TaskSheet?>(null)
        var opened: SessionKey? = null
        var checkedLabel = ""
        rule.mainClock.autoAdvance = false
        try {
            rule.setContent {
                PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                    val context = LocalContext.current
                    // Glass draws Persona's work line as the Agent chat's, as iOS does (#4339 C13)
                    checkedLabel = if (style == AppStyle.Glass) context.resources.getString(R.string.chat_work_tools, 2)
                        else context.resources.getQuantityString(R.plurals.chat_assistant_checked, 2, 2)
                    val loader = remember { ImageLoader.Builder(context).build() }
                    DisposableEffect(loader) { onDispose { loader.shutdown() } }
                    val todos by store.todos.collectAsState()
                    val host = AssistantTaskHost(store, "main", listOf(Session("computer", "agent", state = "working", activity = "Verifying layout",
                        activityAt = "2026-11-15T00:20:00Z")),
                        { it }, { opened = it }, { throw it }, {}, sheet = sheet)
                    val original = ChatTimeline().lines(transcript, false, expanded).lines
                    val presentation = AssistantConversation.project(original, transcript)
                    val lines = if (simplified) AssistantTaskCards.lines(presentation.foreground, feed, mapOf(plan.id to plan), todos)
                        else AssistantLines.lines(original, feed)
                    CompositionLocalProvider(LocalAssistantTaskHost provides host) {
                        Column(Modifier.fillMaxSize().testTag("conversation-shot").background(PersonaTheme.palette.background)
                            .padding(Tokens.Space.l), verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
                            lines.forEach { line -> key(line.contentKey) {
                                ChatLineContent(line, session, loader, null,
                                    onToggle = { id -> expanded = if (id in expanded) expanded - id else expanded + id },
                                    onFold = {}, folding = emptySet(), foldErrors = emptyMap(), onFullText = {},
                                    style = if (simplified) ChatRenderStyle.Assistant else ChatRenderStyle.Agent)
                            } }
                        }
                    }
                }
            }
            fun settle() {
                Snapshot.sendApplyNotifications()
                repeat(4) { rule.mainClock.advanceTimeByFrame() }
                rule.mainClock.advanceTimeBy(500)
            }
            val colors = mutableListOf<Int>()
            for (appearance in listOf(false, true)) {
                dark = appearance; simplified = false; expanded = emptySet(); settle()
                rule.onNodeWithText("No decision needed").assertExists()
                rule.onNodeWithText("Verification passed. Evidence remains available.").assertExists()
                capture(style, appearance, "before", language)
                simplified = true; settle()
                rule.onNodeWithText("No decision needed").assertDoesNotExist()
                rule.onNodeWithText("Context compacted").assertDoesNotExist()
                rule.onNodeWithText("Verification passed. Evidence remains available.").assertDoesNotExist()
                rule.onNodeWithText("Keep the final answer here. Progress stays in its panel.").assertIsDisplayed()
                rule.onNodeWithText(checkedLabel).assertIsDisplayed()
                rule.onNodeWithText("layout", substring = true).assertDoesNotExist()
                rule.onAllNodesWithTag("assistant-dispatch-hint").assertCountEquals(1)
                rule.onAllNodesWithTag("handoff-card").assertCountEquals(1)
                // the card names the Agent and opens that Agent's page, not the task sheet
                rule.onNodeWithText("Layout agent").assertIsDisplayed()
                rule.onNodeWithText("Dispatched:", substring = true).assertDoesNotExist()
                rule.onNodeWithTag("handoff-card").performClick()
                rule.runOnIdle { assertEquals(SessionKey("computer", "agent"), opened); opened = null; assertNull(sheet.value) }
                colors += capture(style, appearance, "after", language)
                rule.onNodeWithText(checkedLabel).performClick(); settle()
                rule.onNodeWithText("layout", substring = true).assertExists()
                capture(style, appearance, "expanded", language)
                rule.onNodeWithText(checkedLabel).performClick(); settle()
                rule.onNodeWithText("layout", substring = true).assertDoesNotExist()
                // A live accepted update changes the task projection, never inserting a second conversation card.
                rule.runOnIdle {
                    api.todo = todo.copy(doneAt = at(12), supervision = Supervision(state = "accepted"))
                    store.reloadTodos()
                }
                settle()
                rule.onAllNodesWithTag("assistant-dispatch-hint").assertCountEquals(1)
                rule.onNodeWithText("Verification passed. Evidence remains available.").assertDoesNotExist()
            }
            assertNotEquals("dark capture must differ from light", colors[0], colors[1])
        } finally { scope.cancel() }
    }

    private fun capture(style: AppStyle, dark: Boolean, phase: String, language: String): Int {
        val directory = File("build/assistant-conversation-shots").also { it.mkdirs() }
        val bitmap = rule.onNodeWithTag("conversation-shot").captureToImage().asAndroidBitmap()
        File(directory, "$phase-${style.name.lowercase()}-${if (dark) "dark" else "light"}-$language.png").outputStream().use {
            assertTrue(bitmap.compress(Bitmap.CompressFormat.PNG, 100, it))
        }
        return bitmap.getPixel(2, 2)
    }

    @Test fun backgroundTaskEventsAreReadOnlyAuditRecords() {
        var style by mutableStateOf(AppStyle.Brief)
        var dark by mutableStateOf(false)
        rule.mainClock.autoAdvance = false
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                val context = LocalContext.current
                val loader = remember { ImageLoader.Builder(context).build() }
                DisposableEffect(loader) { onDispose { loader.shutdown() } }
                Box(Modifier.fillMaxSize().background(PersonaTheme.palette.background)) {
                    AssistantBackgroundRecords(AssistantLines.lines(emptyList(), feed), session, loader,
                        onToggle = {}, onFold = {}, folding = emptySet(), foldErrors = emptyMap(), onFullText = {}, onDismiss = {})
                }
            }
        }
        for (appearance in AppStyle.entries) {
            val backgrounds = mutableListOf<Int>()
            for (night in listOf(false, true)) {
                style = appearance; dark = night
                Snapshot.sendApplyNotifications()
                rule.mainClock.advanceTimeBy(500)
                rule.onNodeWithText("receipt-feed", substring = true).assertExists()
                rule.onNodeWithText("Verification passed. Evidence remains available.", substring = true).assertExists()
                rule.onNodeWithTag("assistant-dispatch-hint").assertDoesNotExist()
                rule.onNodeWithText("Assistant layout", substring = false).assertDoesNotExist()
                val bitmap = rule.runOnIdle { captureWindows() }
                val directory = File("build/assistant-conversation-shots").also { it.mkdirs() }
                File(directory, "audit-${appearance.name.lowercase()}-${if (night) "dark" else "light"}-zh.png").outputStream().use {
                    assertTrue(bitmap.compress(Bitmap.CompressFormat.PNG, 100, it))
                }
                backgrounds += bitmap.getPixel(bitmap.width / 2, bitmap.height - 10)
            }
            assertNotEquals("Audit dark and light must differ", backgrounds[0], backgrounds[1])
        }
    }

    /** Compose sheets have their own window; drawing that real decor avoids captureToImage cropping the Activity. */
    @Suppress("UNCHECKED_CAST")
    private fun captureWindows(): Bitmap {
        val global = Class.forName("android.view.WindowManagerGlobal")
        val instance = global.getMethod("getInstance").invoke(null)
        val views = global.getDeclaredField("mViews").apply { isAccessible = true }.get(instance) as List<android.view.View>
        val bitmap = Bitmap.createBitmap(views.first().width, views.first().height, Bitmap.Config.ARGB_8888)
        val canvas = android.graphics.Canvas(bitmap)
        views.forEach { view ->
            val position = IntArray(2).also { view.getLocationOnScreen(it) }
            canvas.save(); canvas.translate(position[0].toFloat(), position[1].toFloat()); view.draw(canvas); canvas.restore()
        }
        return bitmap
    }

    @Test fun glassBeforeAfterLightDark() = verify(AppStyle.Glass)
    @Test fun briefBeforeAfterLightDark() = verify(AppStyle.Brief)
    @Test fun boardBeforeAfterLightDark() = verify(AppStyle.Board)
    @Test @Config(qualifiers = "en-w400dp-h1200dp-xhdpi")
    fun englishBeforeAfterLightDark() = verify(AppStyle.Brief, "en")
    @Test @Config(qualifiers = "ja-w400dp-h1200dp-xhdpi")
    fun japaneseBeforeAfterLightDark() = verify(AppStyle.Brief, "ja")
}
