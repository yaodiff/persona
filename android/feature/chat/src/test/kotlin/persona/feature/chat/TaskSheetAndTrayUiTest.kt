package persona.feature.chat

import android.content.res.Configuration
import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.padding
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.snapshots.Snapshot
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.LocalConfiguration
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.getUnclippedBoundsInRoot
import androidx.compose.ui.test.hasAnyAncestor
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.isDialog
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performScrollTo
import androidx.compose.ui.unit.Density
import androidx.compose.ui.unit.height
import androidx.test.ext.junit.runners.AndroidJUnit4
import coil3.ImageLoader
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.FeedItem
import persona.core.contracts.FeedResponse
import persona.core.contracts.Item
import persona.core.contracts.Machine
import persona.core.contracts.ServerEvent
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.contracts.TaskPlan
import persona.core.contracts.Todo
import persona.core.contracts.TodoAgent
import persona.core.contracts.TodoAsk
import persona.core.contracts.api.ConnectionState
import persona.core.contracts.api.HubConnection
import persona.core.contracts.api.TasksApi
import persona.core.designsystem.AppStyle
import persona.core.designsystem.AssistantTrayTokens
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.Tokens
import persona.core.domain.lines.ChatLine
import persona.core.domain.lines.LineItem
import persona.core.domain.lines.ToolCall
import persona.core.domain.lines.WorkItem
import persona.core.domain.tasks.TasksStore
import java.io.File
import java.time.Instant

/**
 * UI & preview regression coverage for the refactored Task Detail bottom sheet, expanded Needs-you tray,
 * and zero-tool compact assistant fold line across Glass/Brief/Board, light/dark, en/zh/ja, and 2.0x text scale.
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "zh-w412dp-h915dp-xhdpi", application = QuestionTestApplication::class)
class TaskSheetAndTrayUiTest {
    @get:Rule val rule = createComposeRule()
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.Unconfined)
    @After fun close() { scope.cancel() }

    private val now = Instant.now()
    private val agent = Session(
        machineId = "workstation",
        sessionId = "net-audit",
        title = "Cellular Network Audit",
        cwd = "C:\\Users\\demo\\Workspace\\sample-net",
        state = "unread",
        status = "done",
        agent = "antigravity",
        agentLabelHint = "Antigravity",
        lastText = "1. **P1 修复确认**：[SampleVpnService.kt](file:///C:/Users/demo/Workspace/sample-net/app/src/main/java/dev/example/app/service/SampleVpnService.kt) 校验并限频告警 `protect(fd)` 返回值 + `ParcelFileDescriptor.fromFd(fd).use { net.bindSocket(it.fileDescriptor) }`",
    )
    private val clippedAgent = agent.copy(
        lastText = "1. **P1 修复确认**：[SampleVpnService.kt](file:///C:/Users/demo/Workspace/sample-net/app/src/main/java/dev/example/app/service/SampleVpnService.kt...",
    )
    private val todoA = Todo(
        id = "todo-cellular",
        title = "修复示例客户端蜂窝网络切换",
        createdAt = now.minusSeconds(1800).toString(),
        updatedAt = now.minusSeconds(120).toString(),
        origin = "main",
        agents = listOf(TodoAgent("workstation", "net-audit", cwd = "C:\\Users\\demo\\Workspace\\sample-net")),
        ask = TodoAsk(
            outcome = "done",
            verdict = "✅ 做完了：已合未部署，按下面验收",
            askedAt = now.minusSeconds(120).toString(),
        ),
    )
    private val todoB = Todo(
        id = "todo-release",
        title = "执行版本协调切换并完成双机真机复验",
        createdAt = now.minusSeconds(1600).toString(),
        updatedAt = now.minusSeconds(90).toString(),
        origin = "dev-dir",
        agents = listOf(TodoAgent("workstation", "dev-agent", cwd = "C:\\Users\\demo\\Workspace\\persona")),
        ask = TodoAsk(
            outcome = "done",
            verdict = "✅ 做完了：已部署，按下面验收",
            askedAt = now.minusSeconds(90).toString(),
        ),
    )
    private val todoRunning = Todo(
        id = "todo-running",
        title = "补齐离线缓存单元测试",
        createdAt = now.minusSeconds(600).toString(),
        updatedAt = now.minusSeconds(30).toString(),
        origin = "main",
        agents = listOf(TodoAgent("workstation", "run-1")),
    )
    private val todoFinished = Todo(
        id = "todo-finished",
        title = "更新发布说明",
        createdAt = now.minusSeconds(3600).toString(),
        updatedAt = now.minusSeconds(600).toString(),
        doneAt = now.minusSeconds(600).toString(),
        origin = "main",
    )
    private val mainSession = Session("workstation", "main", title = "Persona", assistant = true, state = "idle", status = "idle")
    private val devDirSession = Session("workstation", "dev-dir", title = "Persona 研发", assistant = true, state = "idle", status = "idle")
    private val runSession = Session("workstation", "run-1", title = "补齐离线缓存单元测试", state = "working", status = "working", activity = "正在运行单测")
    private val devAgentSession = Session("workstation", "dev-agent", title = "双机真机复验", state = "unread", status = "done")
    private val machines = listOf(Machine("workstation", "Workstation", online = true))

    private fun makeStore(
        todos: List<Todo> = listOf(todoA, todoB, todoRunning, todoFinished),
        accepted: MutableList<Pair<String, String>> = mutableListOf(),
    ): TasksStore {
        val staleReceipt = FeedItem(
            id = "r1",
            conversation = "main",
            at = now.minusSeconds(180).toString(),
            kind = "receipt",
            ref = todoA.id,
            body = buildJsonObject {
                put("state", "done")
                put("summary", agent.lastText.orEmpty())
            },
        )
        val api = object : TasksApi {
            override suspend fun todos() = todos
            override suspend fun feed(conversation: String, since: String?) = FeedResponse(listOf(staleReceipt))
            override suspend fun startPlan(id: String, remember: Boolean): TaskPlan = error("unused")
            override suspend fun dismissPlan(id: String): TaskPlan = error("unused")
            override suspend fun retryTask(todoId: String): Todo = error("unused")
            override suspend fun removeTask(todoId: String) = Unit
            override suspend fun route(conversation: String, todoId: String, text: String): FeedItem = error("unused")
            override suspend fun today(conversation: String): FeedItem = error("unused")
            override suspend fun mainConversation() = "main"
            override suspend fun inboxReply(itemId: String, text: String, card: String?) = Unit
            override suspend fun acceptTask(todoId: String, askedAt: String) {
                accepted += todoId to askedAt
            }
        }
        val conn = object : HubConnection {
            override val state = MutableStateFlow(ConnectionState.WaitingForNetwork)
            override val events = MutableSharedFlow<ServerEvent>()
            override fun reconnect() = Unit
            override fun send(message: JsonObject) = true
        }
        return TasksStore(api, conn, scope).also {
            it.reloadTodos()
            it.load("main")
            it.loadTodayIfMain("other")
        }
    }

    private fun savePreviewIfRequested(name: String, bitmap: Bitmap) {
        val dir = System.getProperty("persona.previewDir")?.takeIf(String::isNotBlank) ?: "build/task-sheet-tray-preview"
        val out = File(dir).also { it.mkdirs() }
        File(out, "$name.png").outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
    }

    private fun settle() {
        Snapshot.sendApplyNotifications()
        repeat(10) { rule.mainClock.advanceTimeByFrame() }
        rule.mainClock.advanceTimeBy(1_500)
        rule.waitForIdle()
    }

    private fun renderSheet(style: AppStyle, dark: Boolean, fontScale: Float = 1f, clipped: Boolean = false, previewName: String? = null) {
        val accepted = mutableListOf<Pair<String, String>>()
        val store = makeStore(accepted = accepted)
        rule.mainClock.autoAdvance = false
        rule.setContent {
            val baseConfig = LocalConfiguration.current
            val configuration = remember(baseConfig, dark, fontScale) {
                Configuration(baseConfig).apply {
                    this.fontScale = fontScale
                    uiMode = (uiMode and Configuration.UI_MODE_NIGHT_MASK.inv()) or
                        if (dark) Configuration.UI_MODE_NIGHT_YES else Configuration.UI_MODE_NIGHT_NO
                }
            }
            val density = Density(LocalDensity.current.density, fontScale)
            CompositionLocalProvider(LocalConfiguration provides configuration, LocalDensity provides density) {
                PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                    val sheet = remember { mutableStateOf<TaskSheet?>(TaskSheet.Task(todoA.id)) }
                    val host = AssistantTaskHost(
                        store,
                        "main",
                        listOf(mainSession, devDirSession, if (clipped) clippedAgent else agent, devAgentSession, runSession),
                        { "Workstation" },
                        {},
                        {},
                        {},
                        sheet = sheet,
                        machines = machines,
                    )
                    Box(Modifier.fillMaxSize().background(PersonaTheme.palette.background))
                    AssistantTaskSheets(host)
                }
            }
        }
        settle()
        // No contradictory "stopped, not done yet" card alongside Done/Pending-acceptance.
        assertTrue(rule.onAllNodesWithText("停下了，还没完成", substring = true, useUnmergedTree = true).fetchSemanticsNodes().isEmpty())
        // No raw Windows path, no raw file:/// link, no raw Markdown ** markers, no raw ✅ emoji.
        assertTrue(rule.onAllNodesWithText("C:\\Users", substring = true, useUnmergedTree = true).fetchSemanticsNodes().isEmpty())
        assertTrue(rule.onAllNodesWithText("file:///", substring = true, useUnmergedTree = true).fetchSemanticsNodes().isEmpty())
        assertTrue(rule.onAllNodesWithText("**P1", substring = true, useUnmergedTree = true).fetchSemanticsNodes().isEmpty())
        assertTrue(rule.onAllNodesWithText("✅", substring = true, useUnmergedTree = true).fetchSemanticsNodes().isEmpty())
        // Compact metadata & clean summary & single primary focus + demoted secondary action bar.
        rule.onNodeWithTag("task-detail-agent-meta").assertIsDisplayed()
        rule.onNode(hasText("Workstation · sample-net · Antigravity") and hasAnyAncestor(isDialog())).assertIsDisplayed()
        rule.onNode(hasText("做完了：已合未部署，按下面验收") and hasAnyAncestor(isDialog())).assertIsDisplayed()
        rule.onNodeWithTag("assistant-accept-${todoA.id}").assertIsDisplayed()
        rule.onNodeWithTag("task-detail-actions-secondary").assertIsDisplayed()
        rule.onNodeWithTag("task-detail-open-chat").assertIsDisplayed()
        rule.onNodeWithTag("task-detail-say").assertIsDisplayed()
        if (previewName != null) {
            savePreviewIfRequested(previewName, rule.onNode(isDialog()).captureToImage().asAndroidBitmap())
        }
        rule.onNodeWithTag("assistant-accept-${todoA.id}").performClick()
        settle()
        assertEquals(listOf(todoA.id to todoA.ask!!.askedAt), accepted)
    }

    private fun renderNeedsTray(style: AppStyle, dark: Boolean, fontScale: Float = 1f, previewName: String? = null) {
        val store = makeStore()
        rule.mainClock.autoAdvance = false
        rule.setContent {
            val baseConfig = LocalConfiguration.current
            val configuration = remember(baseConfig, dark, fontScale) {
                Configuration(baseConfig).apply {
                    this.fontScale = fontScale
                    uiMode = (uiMode and Configuration.UI_MODE_NIGHT_MASK.inv()) or
                        if (dark) Configuration.UI_MODE_NIGHT_YES else Configuration.UI_MODE_NIGHT_NO
                }
            }
            val density = Density(LocalDensity.current.density, fontScale)
            CompositionLocalProvider(LocalConfiguration provides configuration, LocalDensity provides density) {
                PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                    val host = AssistantTaskHost(
                        store,
                        "main",
                        listOf(mainSession, devDirSession, agent, devAgentSession, runSession),
                        { "Workstation" },
                        {},
                        {},
                        {},
                        machines = machines,
                    )
                    Column(
                        Modifier.fillMaxSize().background(PersonaTheme.palette.background).testTag("after-tray-shot")
                            .padding(vertical = Tokens.Space.l),
                    ) {
                        Spacer(Modifier.weight(1f))
                        AssistantTrays(
                            host,
                            remember { AssistantTrayState().apply { expanded = AssistantTray.NeedsYou } },
                            emptyList(),
                            emptyList(),
                            emptyList(),
                            { AssistantTrayTokens.maximumHeight },
                            busy = false,
                            enabled = true,
                            onApprove = {},
                            onReview = {},
                            onSend = {},
                            onSchedule = {},
                            groupByOrigin = true,
                        )
                    }
                }
            }
        }
        settle()
        // No system emoji clash and no misleading "按下面验收" copy inside tray rows.
        assertTrue(rule.onAllNodesWithText("✅", substring = true, useUnmergedTree = true).fetchSemanticsNodes().isEmpty())
        assertTrue(rule.onAllNodesWithText("按下面验收", substring = true, useUnmergedTree = true).fetchSemanticsNodes().isEmpty())
        rule.onNodeWithTag("assistant-tray-header").assertIsDisplayed()
        rule.onNodeWithTag("assistant-needs-group-main").assertIsDisplayed()
        rule.onNodeWithTag("assistant-needs-group-dev-dir").assertIsDisplayed()
        rule.onNodeWithText("做完了：已合未部署，待验收").assertIsDisplayed()
        rule.onNodeWithText("做完了：已部署，待验收").assertIsDisplayed()
        val cardBounds = rule.onNodeWithTag("assistant-tray-needs").getUnclippedBoundsInRoot()
        val screenBounds = rule.onNodeWithTag("after-tray-shot").getUnclippedBoundsInRoot()
        if (fontScale <= 1.0f) {
            // 2 items across 2 groups must stay compact and never eat half the screen.
            assertTrue("Tray card too tall: ${cardBounds.height} on ${screenBounds.height}", cardBounds.height < screenBounds.height * 0.36f)
        }
        if (previewName != null) {
            savePreviewIfRequested(previewName, rule.onNodeWithTag("after-tray-shot").captureToImage().asAndroidBitmap())
        }
    }

    @Test fun taskSheetBriefDark() = renderSheet(AppStyle.Brief, dark = true, previewName = "after-task-sheet-brief-dark")
    @Test fun taskSheetBriefLight() = renderSheet(AppStyle.Brief, dark = false, previewName = "after-task-sheet-brief-light")
    @Test fun taskSheetGlassDark() = renderSheet(AppStyle.Glass, dark = true, previewName = "after-task-sheet-glass-dark")
    @Test fun taskSheetGlassLight() = renderSheet(AppStyle.Glass, dark = false, previewName = "after-task-sheet-glass-light")
    @Test fun taskSheetBoardDark() = renderSheet(AppStyle.Board, dark = true, previewName = "after-task-sheet-board-dark")
    @Test fun taskSheetBoardLight() = renderSheet(AppStyle.Board, dark = false, previewName = "after-task-sheet-board-light")
    @Test fun taskSheetBriefDarkLargeFont() = renderSheet(AppStyle.Brief, dark = true, fontScale = 2.0f, clipped = true, previewName = "after-task-sheet-brief-dark-2x")

    @Test fun needsTrayBriefDark() = renderNeedsTray(AppStyle.Brief, dark = true, previewName = "after-needs-tray-brief-dark")
    @Test fun needsTrayBriefLight() = renderNeedsTray(AppStyle.Brief, dark = false, previewName = "after-needs-tray-brief-light")
    @Test fun needsTrayGlassDark() = renderNeedsTray(AppStyle.Glass, dark = true, previewName = "after-needs-tray-glass-dark")
    @Test fun needsTrayGlassLight() = renderNeedsTray(AppStyle.Glass, dark = false, previewName = "after-needs-tray-glass-light")
    @Test fun needsTrayBoardDark() = renderNeedsTray(AppStyle.Board, dark = true, previewName = "after-needs-tray-board-dark")
    @Test fun needsTrayBoardLight() = renderNeedsTray(AppStyle.Board, dark = false, previewName = "after-needs-tray-board-light")
    @Test fun needsTrayBriefDarkLargeFont() = renderNeedsTray(AppStyle.Brief, dark = true, fontScale = 2.0f, previewName = "after-needs-tray-brief-dark-2x")

    @Test
    @Config(sdk = [35], qualifiers = "en-w412dp-h915dp-xhdpi", application = QuestionTestApplication::class)
    fun needsTrayEnglishAdaptsBelowVerdictCleanly() {
        val enTodoA = todoA.copy(ask = TodoAsk("done", "✅ Done: merged, not deployed yet; check it below", askedAt = now.toString()))
        val enTodoB = todoB.copy(ask = TodoAsk("done", "✅ Done: deployed, check it below", askedAt = now.toString()))
        val store = makeStore(todos = listOf(enTodoA, enTodoB, todoRunning, todoFinished))
        rule.mainClock.autoAdvance = false
        rule.setContent {
            PersonaTheme(darkTheme = true, reduceMotion = true, style = AppStyle.Brief) {
                val host = AssistantTaskHost(store, "main", listOf(mainSession, devDirSession, agent, devAgentSession, runSession), { "Workstation" }, {}, {}, {}, machines = machines)
                AssistantTrays(host, remember { AssistantTrayState().apply { expanded = AssistantTray.NeedsYou } },
                    emptyList(), emptyList(), emptyList(), { AssistantTrayTokens.maximumHeight },
                    busy = false, enabled = true, onApprove = {}, onReview = {}, onSend = {}, onSchedule = {}, groupByOrigin = true)
            }
        }
        settle()
        assertTrue(rule.onAllNodesWithText("check it below", substring = true, useUnmergedTree = true).fetchSemanticsNodes().isEmpty())
        rule.onNodeWithText("Done: merged, not deployed yet, ready to verify").assertIsDisplayed()
        rule.onNodeWithText("Done: deployed, ready to verify").assertIsDisplayed()
    }

    @Test
    @Config(sdk = [35], qualifiers = "ja-w412dp-h915dp-xhdpi", application = QuestionTestApplication::class)
    fun needsTrayJapaneseAdaptsBelowVerdictAndLargeFontDoesNotClipSheetActions() {
        val jaTodoA = todoA.copy(ask = TodoAsk("done", "✅ 完了：マージ済み・未デプロイ。下の手順で確認してください", askedAt = now.toString()))
        val store = makeStore(todos = listOf(jaTodoA, todoRunning, todoFinished))
        rule.setContent {
            val baseConfig = LocalConfiguration.current
            val configuration = remember(baseConfig) { Configuration(baseConfig).apply { fontScale = 2.0f } }
            CompositionLocalProvider(LocalConfiguration provides configuration, LocalDensity provides Density(LocalDensity.current.density, 2.0f)) {
                PersonaTheme(darkTheme = false, reduceMotion = true, style = AppStyle.Board) {
                    val sheet = remember { mutableStateOf<TaskSheet?>(null) }
                    val host = AssistantTaskHost(store, "main", listOf(mainSession, agent, runSession), { "Workstation" }, {}, {}, {}, sheet = sheet, machines = machines)
                    Box(Modifier.fillMaxSize().background(PersonaTheme.palette.background)) {
                        AssistantTrays(host, remember { AssistantTrayState().apply { expanded = AssistantTray.NeedsYou } },
                            emptyList(), emptyList(), emptyList(), { AssistantTrayTokens.maximumHeight },
                            busy = false, enabled = true, onApprove = {}, onReview = {}, onSend = {}, onSchedule = {}, groupByOrigin = true)
                    }
                    AssistantTaskSheets(host)
                }
            }
        }
        rule.waitForIdle()
        assertTrue(rule.onAllNodesWithText("下の手順で確認してください", substring = true, useUnmergedTree = true).fetchSemanticsNodes().isEmpty())
        rule.onNodeWithText("完了：マージ済み・未デプロイ・確認待ち").assertIsDisplayed()
        rule.onNodeWithText(jaTodoA.title).performClick()
        rule.waitForIdle()
        rule.onNodeWithTag("assistant-accept-${jaTodoA.id}").performScrollTo().assertIsDisplayed()
        rule.onNodeWithTag("task-detail-open-chat").performScrollTo().assertIsDisplayed()
        rule.onNodeWithTag("task-detail-say").performScrollTo().assertIsDisplayed()
    }

    @Test fun compactWorkSummaryNeverRendersZeroCheckedTools() {
        val thinkingLine = ChatLine.Work(
            id = "w-think",
            steps = listOf(WorkItem.Thought(LineItem(Item("t1", 1, role = "assistant", kind = "thinking", text = "Analyzing")))),
            live = true,
            start = now,
            end = null,
            open = false,
        )
        val emptyLive = ChatLine.Work(id = "w-empty", steps = emptyList(), live = true, start = now, end = null, open = false)
        val narrationLine = ChatLine.Work(
            id = "w-narr",
            steps = listOf(WorkItem.Narration(LineItem(Item("n1", 2, role = "assistant", kind = "text", text = "Checking")))),
            live = false,
            start = now,
            end = now,
            open = false,
        )
        val twoTools = ChatLine.Work(
            id = "w-tools",
            steps = listOf(
                WorkItem.Tool(ToolCall(LineItem(Item("u1", 3, role = "assistant", kind = "tool_use", toolName = "Read")), null, emptyList())),
                WorkItem.Tool(ToolCall(LineItem(Item("u2", 4, role = "assistant", kind = "tool_use", toolName = "Grep")), null, emptyList())),
            ),
            live = false,
            start = now,
            end = now,
            open = false,
        )
        val sessionKey = SessionKey("workstation", "main")
        rule.setContent {
            val context = LocalContext.current
            val loader = remember(context) { ImageLoader.Builder(context).build() }
            PersonaTheme(darkTheme = true, reduceMotion = true, style = AppStyle.Brief) {
                Column {
                    listOf(thinkingLine, emptyLive, narrationLine, twoTools).forEach { line ->
                        ChatLineContent(
                            line = line,
                            session = sessionKey,
                            loader = loader,
                            recordedDuration = null,
                            onToggle = {},
                            onFold = {},
                            folding = emptySet(),
                            foldErrors = emptyMap(),
                            onFullText = {},
                            style = ChatRenderStyle.Assistant,
                        )
                    }
                }
            }
        }
        rule.waitForIdle()
        assertTrue(rule.onAllNodesWithText("查了 0 处", substring = true, useUnmergedTree = true).fetchSemanticsNodes().isEmpty())
        rule.onNodeWithText("查了 2 处").assertIsDisplayed()
        rule.onNodeWithText("回复 1").assertIsDisplayed()
    }
}
