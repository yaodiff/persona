package persona.feature.chat

import android.content.res.Configuration
import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.key
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.runtime.snapshots.Snapshot
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.LocalConfiguration
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.getUnclippedBoundsInRoot
import androidx.compose.ui.test.hasSetTextAction
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithContentDescription
import androidx.compose.ui.test.onAllNodesWithTag
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithContentDescription
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.text.input.TextFieldValue
import androidx.compose.ui.unit.Density
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.height
import androidx.compose.ui.unit.width
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.flowOf
import kotlinx.serialization.json.JsonObject
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
import persona.core.contracts.Interaction
import persona.core.contracts.Item
import persona.core.contracts.JsonCodec
import persona.core.contracts.LnModeResponse
import persona.core.contracts.Machine
import persona.core.contracts.PushIntercept
import persona.core.contracts.PushInterceptsResponse
import persona.core.contracts.QueueEdit
import persona.core.contracts.Resolution
import persona.core.contracts.ServerEvent
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.contracts.TaskPlan
import persona.core.contracts.Todo
import persona.core.contracts.TodoAgent
import persona.core.contracts.api.AssistantConfig
import persona.core.contracts.api.AssistantManagement
import persona.core.contracts.api.AssistantNotes
import persona.core.contracts.api.ChatAction
import persona.core.contracts.api.ChatActionResult
import persona.core.contracts.api.ChatActions
import persona.core.contracts.api.ChatRepository
import persona.core.contracts.api.ChatSnapshot
import persona.core.contracts.api.ConnectionState
import persona.core.contracts.api.HubConnection
import persona.core.contracts.api.InteractionCenter
import persona.core.contracts.api.MemoryFile
import persona.core.contracts.api.MessageSender
import persona.core.contracts.api.OutboxMessage
import persona.core.contracts.api.Run
import persona.core.contracts.api.ScheduledTask
import persona.core.contracts.api.SessionsRepository
import persona.core.contracts.api.TasksApi
import persona.core.designsystem.AppStyle
import persona.core.designsystem.HomeChrome
import persona.core.designsystem.JChipTokens
import persona.core.designsystem.LocalHomeChrome
import persona.core.designsystem.NavItem
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.Tokens
import persona.core.designsystem.icons.PhIcons
import persona.core.domain.tasks.TasksStore
import java.io.File
import java.time.Instant

/**
 * Verifies the compacted main-chat bottom chrome stack (status tray capsules, queued message strip, launcher/mode
 * chips, input field, and expanded quick-action toolbar) across Glass, Brief, and Board, in light/dark themes, at
 * 1.0x and 2.0x font scale, and in a short IME-constrained viewport.
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h760dp-xhdpi")
class ChatBottomChromeCompactTest {
    @get:Rule val rule = createComposeRule()
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.Main.immediate)

    @After
    fun tearDown() {
        scope.cancel()
    }

    private val main = SessionKey("computer-a", "main")
    private val nowIso = Instant.now().minusSeconds(1800).toString()
    private val running = Session(
        "computer-a", "work-1", title = "Check API migration", cwd = "/work/app",
        state = "working", status = "working", activity = "Running tests", updatedAt = nowIso,
    )
    private val blocked = Session(
        "computer-a", "need-1", title = "Confirm rollout", cwd = "/work/app",
        state = "needs_you", status = "blocked", attentionReason = "Waiting for confirmation", updatedAt = nowIso,
    )
    private val todos = listOf(
        Todo("t-need", blocked.title, origin = "main", createdAt = nowIso, updatedAt = nowIso,
            agents = listOf(TodoAgent(blocked.machineId, blocked.sessionId, cwd = blocked.cwd))),
        Todo("t-run", running.title, origin = "main", createdAt = nowIso, updatedAt = nowIso,
            agents = listOf(TodoAgent(running.machineId, running.sessionId, cwd = running.cwd))),
        Todo("t-done", "Summarize notes", origin = "main", createdAt = nowIso, updatedAt = nowIso, doneAt = nowIso),
    )
    private val personaSession = JsonCodec.decodeFromString<Session>(
        """{"machine_id":"computer-a","session_id":"main","title":"Persona","assistant":true,"agent":"claude",
            "launcher":"agy","permission_mode":"bypassPermissions","effort":"medium","status":"working","state":"working",
            "queued":["Check the release checklist and regression results"]}""",
    )

    private val api = object : TasksApi {
        override suspend fun todos() = todos
        override suspend fun feed(conversation: String, since: String?) = FeedResponse(emptyList())
        override suspend fun startPlan(id: String, remember: Boolean): TaskPlan = error("not used")
        override suspend fun dismissPlan(id: String): TaskPlan = error("not used")
        override suspend fun retryTask(todoId: String): Todo = error("not used")
        override suspend fun removeTask(todoId: String) = Unit
        override suspend fun route(conversation: String, todoId: String, text: String): FeedItem = error("not used")
        override suspend fun today(conversation: String): FeedItem = error("not used")
        override suspend fun mainConversation() = "main"
        override suspend fun inboxReply(itemId: String, text: String, card: String?) = Unit
    }
    private val connection = object : HubConnection {
        override val state = MutableStateFlow(ConnectionState.Online)
        override val events = MutableSharedFlow<ServerEvent>()
        override fun reconnect() = Unit
        override fun send(message: JsonObject) = true
    }
    private val sessions = object : SessionsRepository {
        override val sessions = MutableStateFlow(listOf(personaSession, running, blocked))
        override val machines = MutableStateFlow(listOf(Machine("computer-a", "Computer A", online = true)))
        override suspend fun refresh() = Unit
    }
    private val interactions = object : InteractionCenter {
        override val pending = MutableStateFlow(emptyList<Interaction>())
        override suspend fun refresh() = Unit
        override suspend fun resolve(id: String, resolution: Resolution) = Unit
    }
    private val chats = object : ChatRepository {
        override fun observe(session: SessionKey) = flowOf(
            ChatSnapshot(
                loading = false,
                items = listOf(
                    Item("u1", 1, role = "user", kind = "text", text = "Please check this change."),
                    Item("a1", 2, role = "assistant", kind = "text", text = "Reviewing the latest commit and test results now."),
                ),
            ),
        )
        override suspend fun loadOlder(session: SessionKey) = Unit
        override suspend fun expandFold(session: SessionKey, fromSeq: Int, toSeq: Int) = emptyList<Item>()
    }
    private val sender = object : MessageSender {
        override val outbox = MutableStateFlow(emptyList<OutboxMessage>())
        override suspend fun send(session: SessionKey, text: String, clientMessageId: String, clearDraft: String?) = Unit
    }
    private val actions = object : ChatActions {
        override suspend fun isMuted(session: Session) = false
        override suspend fun execute(session: Session, action: ChatAction): ChatActionResult = ChatActionResult.Done
        override suspend fun verify(session: Session, action: ChatAction): ChatActionResult = ChatActionResult.Done
        override suspend fun editQueue(session: Session, keep: List<Int>): QueueEdit = QueueEdit(session.queued.orEmpty())
        override suspend fun sendQueuedNow(session: Session) = Unit
    }
    private val management = object : AssistantManagement {
        override suspend fun config() = AssistantConfig(main = "main", launcher = "agy")
        override suspend fun tasks() = emptyList<ScheduledTask>()
        override suspend fun warm() = Unit
        override suspend fun endConversation(session: SessionKey) = Unit
        override suspend fun saveTask(task: ScheduledTask) = task
        override suspend fun deleteTask(id: String) = Unit
        override suspend fun runTask(id: String) = main
        override suspend fun freshTask(id: String) = ScheduledTask(id)
        override suspend fun runs() = emptyList<Run>()
        override suspend fun notes() = AssistantNotes()
        override suspend fun setNotes(text: String) = Unit
        override suspend fun memory() = emptyList<MemoryFile>()
        override suspend fun saveMemory(name: String, content: String) = Unit
        override suspend fun deleteMemory(name: String) = Unit
    }

    private fun settle() {
        Snapshot.sendApplyNotifications()
        repeat(5) { rule.mainClock.advanceTimeByFrame() }
        rule.mainClock.advanceTimeBy(300)
        rule.waitForIdle()
    }

    private fun saveShot(name: String) {
        val output = File(System.getProperty("persona.shots") ?: "build/bottom-chrome-compact-shots").also { it.mkdirs() }
        val bitmap = rule.onNodeWithTag("bottom-chrome-shot").captureToImage().asAndroidBitmap()
        File(output, "$name.png").outputStream().use {
            assertTrue(bitmap.compress(Bitmap.CompressFormat.PNG, 100, it))
        }
    }

    @Test
    fun stackedBottomChromeStaysCompactUnclippedAndAccessibleAcrossStylesLargeTextAndIme() {
        val tasks = TasksStore(api, connection, scope).also { it.reloadTodos() }
        var style by mutableStateOf(AppStyle.Brief)
        var dark by mutableStateOf(true)
        var fontScale by mutableStateOf(1f)
        var viewportHeight by mutableStateOf(760)
        val composer = ChatComposerState().apply {
            focused = true
            value = TextFieldValue("")
        }
        rule.mainClock.autoAdvance = false
        rule.setContent {
            val baseDensity = LocalDensity.current
            val configuration = Configuration(LocalConfiguration.current).apply {
                this.fontScale = fontScale
                screenHeightDp = viewportHeight
                uiMode = (uiMode and Configuration.UI_MODE_NIGHT_MASK.inv()) or
                    if (dark) Configuration.UI_MODE_NIGHT_YES else Configuration.UI_MODE_NIGHT_NO
            }
            CompositionLocalProvider(
                LocalConfiguration provides configuration,
                LocalDensity provides Density(baseDensity.density, fontScale),
            ) {
                PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                    Box(
                        Modifier.width(400.dp).height(viewportHeight.dp)
                            .background(PersonaTheme.palette.background)
                            .testTag("bottom-chrome-shot"),
                    ) {
                        key(style, dark, fontScale, viewportHeight) {
                            ChatScreen(
                                main, chats, sender, interactions, sessions, {},
                                chrome = ChatChromeBindings(
                                    actions = actions,
                                    connection = connection,
                                    tasks = tasks,
                                    assistantManagement = management,
                                    onOpenSession = {},
                                ),
                                renderStyle = ChatRenderStyle.Assistant,
                                embedded = true,
                                showTopBar = false,
                                composerState = composer,
                            )
                        }
                    }
                }
            }
        }
        settle()
        rule.waitUntil(10_000) { rule.onAllNodesWithTag("assistant-tray-bar").fetchSemanticsNodes().isNotEmpty() }

        // 1. Standard 1.0x across all 3 styles, light & dark (with expanded composer toolbar and queued message).
        for (s in AppStyle.entries) for (isDark in listOf(false, true)) {
            style = s; dark = isDark; fontScale = 1f; viewportHeight = 760
            settle()
            rule.onNode(hasSetTextAction()).performClick()
            settle()
            // Expand Glass queue so we inspect both folded and expanded states.
            if (s == AppStyle.Glass) {
                saveShot("stacked-folded-${s.name.lowercase()}-${if (isDark) "dark" else "light"}")
                rule.onAllNodesWithContentDescription("queued · Persona", substring = true)[0].performClick()
                settle()
            }
            val trayBar = rule.onNodeWithTag("assistant-tray-bar").getUnclippedBoundsInRoot()
            val trayEntry = rule.onNodeWithTag("assistant-entry-needs").getUnclippedBoundsInRoot()
            val trayEntryTouch = rule.onNodeWithTag("assistant-entry-needs").fetchSemanticsNode().touchBoundsInRoot
            val queueItem = rule.onNodeWithContentDescription("Item 1: Check the release checklist and regression results").getUnclippedBoundsInRoot()
            val field = rule.onNode(hasSetTextAction()).getUnclippedBoundsInRoot()
            val addNode = rule.onNodeWithContentDescription("Add attachments and quick messages")
            val add = addNode.getUnclippedBoundsInRoot()
            val addTouch = addNode.fetchSemanticsNode().touchBoundsInRoot
            val root = rule.onNodeWithTag("bottom-chrome-shot").getUnclippedBoundsInRoot()
            val minTouchPx = with(rule.density) { Tokens.MinTouch.android.toPx() }

            // Tray bar height matches the visible entry pill (no +6/+9dp minimumInteractiveComponentSize layout bloat).
            assertEquals("$s dark=$isDark: tray bar matches visible entry height", trayEntry.height.value, trayBar.height.value, 0.5f)
            assertTrue("$s dark=$isDark: tray bar height (${trayBar.height}) < 48dp", trayBar.height < Tokens.MinTouch.android)
            assertTrue("$s dark=$isDark: tray entry keeps 48dp touch height", trayEntryTouch.height >= minTouchPx - 0.5f)

            assertTrue("$s dark=$isDark: tray bar above queue row", trayBar.bottom <= queueItem.top)
            assertTrue("$s dark=$isDark: queue row above input field", queueItem.bottom <= field.top)
            assertTrue("$s dark=$isDark: input field above bottom toolbar", field.bottom <= add.top)
            assertTrue("$s dark=$isDark: add button touch width >= 48dp", addTouch.width >= minTouchPx - 0.5f)
            assertTrue("$s dark=$isDark: add button touch height >= 48dp", addTouch.height >= minTouchPx - 0.5f)
            if (s != AppStyle.Glass) {
                val launcherChip = rule.onNodeWithText("Default: agy").getUnclippedBoundsInRoot()
                val launcherTouch = rule.onNodeWithText("Default: agy").fetchSemanticsNode().touchBoundsInRoot
                val modeChip = rule.onNodeWithTag("chat-composer-mode-hint").getUnclippedBoundsInRoot()
                val queueToLauncher = launcherChip.top - queueItem.bottom
                val chipRowToFieldText = field.top - launcherChip.bottom
                assertTrue("$s dark=$isDark: launcher meta chip (${launcherChip.height}) is slimmer than 36dp JChip and >= ${Tokens.Size.wellXS}",
                    launcherChip.height < JChipTokens.height && launcherChip.height >= Tokens.Size.wellXS - 0.5.dp)
                assertEquals("$s dark=$isDark: mode hint chip matches launcher visual height", launcherChip.height.value, modeChip.height.value, 0.5f)
                assertTrue("$s dark=$isDark: launcher chip keeps 48dp touch height", launcherTouch.height >= minTouchPx - 0.5f)
                // Queue row bottom includes 2dp card bottom + 2dp strip bottom + 4dp composer top = 8dp (down from 14-16dp).
                assertTrue("$s dark=$isDark: queue-to-launcher gap ($queueToLauncher) in 4..9dp", queueToLauncher in 4.dp..9.dp)
                // Launcher and mode chips share one flush row with matching top/bottom visual bounds (#13661) and no 48dp box bloat.
                assertEquals("$s dark=$isDark: launcher and mode chips align on top edge", launcherChip.top.value, modeChip.top.value, 0.5f)
                assertEquals("$s dark=$isDark: launcher and mode chips align on bottom edge", launcherChip.bottom.value, modeChip.bottom.value, 0.5f)
                assertTrue("$s dark=$isDark: mode chip sits beside launcher without overlap", modeChip.left >= launcherChip.right + Tokens.Space.s - 0.5.dp)
                assertTrue("$s dark=$isDark: chip row sits tight above input field ($chipRowToFieldText)", chipRowToFieldText in 4.dp..20.dp)
                // Expanded toolbar uses 36dp vertical layout box with 4dp top/bottom breathing room and 48dp width.
                assertEquals("$s dark=$isDark: toolbar button height is balanced 36dp", JChipTokens.height.value, add.height.value, 0.5f)
                assertEquals("$s dark=$isDark: toolbar button width stays 48dp", Tokens.MinTouch.android.value, add.width.value, 0.5f)
                assertTrue("$s dark=$isDark: toolbar keeps >= 4dp bottom breathing room above keyboard", root.bottom - add.bottom >= Tokens.Space.xs - 0.5.dp)
            }
            val minQueueRow = if (s == AppStyle.Glass) JChipTokens.height else Tokens.Size.slimBar
            assertTrue("$s dark=$isDark: queue row height >= $minQueueRow", queueItem.height >= minQueueRow)
            assertTrue("$s dark=$isDark: toolbar stays inside viewport", add.bottom <= root.bottom)

            // Total bottom chrome height drops further once 48dp layout-box bloat is removed.
            val totalChrome = root.bottom - trayBar.top
            val maxAllowed = if (s == AppStyle.Glass) 275.dp else 285.dp
            assertTrue("$s dark=$isDark: total bottom chrome ($totalChrome) <= $maxAllowed", totalChrome <= maxAllowed)

            // Transcript remains clearly visible above the bottom stack.
            val reply = rule.onNodeWithText("Reviewing the latest commit and test results now.").getUnclippedBoundsInRoot()
            assertTrue("$s dark=$isDark: transcript sits above tray bar", reply.bottom <= trayBar.top)

            saveShot("stacked-open-${s.name.lowercase()}-${if (isDark) "dark" else "light"}")
        }

        // 2. 2.0x system font scale across all 3 styles (no overlap, no clipping).
        for (s in AppStyle.entries) {
            style = s; dark = true; fontScale = 2f; viewportHeight = 760
            settle()
            rule.onNode(hasSetTextAction()).performClick()
            settle()
            if (s == AppStyle.Glass) {
                rule.onAllNodesWithContentDescription("queued · Persona", substring = true)[0].performClick()
                settle()
            }
            val trayBar = rule.onNodeWithTag("assistant-tray-bar").getUnclippedBoundsInRoot()
            val queueItem = rule.onNodeWithContentDescription("Item 1: Check the release checklist and regression results").getUnclippedBoundsInRoot()
            val field = rule.onNode(hasSetTextAction()).getUnclippedBoundsInRoot()
            val add = rule.onNodeWithContentDescription("Add attachments and quick messages").getUnclippedBoundsInRoot()
            val root = rule.onNodeWithTag("bottom-chrome-shot").getUnclippedBoundsInRoot()
            assertTrue("$s 2.0x: tray bar above queue row", trayBar.bottom <= queueItem.top)
            assertTrue("$s 2.0x: queue row above input field", queueItem.bottom <= field.top)
            assertTrue("$s 2.0x: input field above bottom toolbar", field.bottom <= add.top)
            assertTrue("$s 2.0x: add button stays inside viewport", add.bottom <= root.bottom)
            rule.onNodeWithContentDescription("Add attachments and quick messages").assertIsDisplayed()
            saveShot("stacked-large-2x-${s.name.lowercase()}-dark")
        }

        // 3. Short IME-constrained viewport (420dp remaining height) across all 3 styles.
        for (s in AppStyle.entries) {
            style = s; dark = true; fontScale = 1f; viewportHeight = 420
            settle()
            rule.onNode(hasSetTextAction()).performClick()
            settle()
            val trayBar = rule.onNodeWithTag("assistant-tray-bar").getUnclippedBoundsInRoot()
            val field = rule.onNode(hasSetTextAction()).getUnclippedBoundsInRoot()
            val add = rule.onNodeWithContentDescription("Add attachments and quick messages").getUnclippedBoundsInRoot()
            val root = rule.onNodeWithTag("bottom-chrome-shot").getUnclippedBoundsInRoot()
            assertTrue("$s IME: tray bar stays inside viewport top", trayBar.top >= root.top)
            assertTrue("$s IME: tray bar above input field", trayBar.bottom <= field.top)
            assertTrue("$s IME: input field above bottom toolbar", field.bottom <= add.top)
            assertTrue("$s IME: bottom toolbar stays inside viewport", add.bottom <= root.bottom)
            rule.onNodeWithContentDescription("Add attachments and quick messages").assertIsDisplayed()
            saveShot("stacked-ime-${s.name.lowercase()}-dark")
        }
    }

    @Test
    fun noQueueBottomChromeMaintainsTightSurfaceGapsAcrossStyles() {
        val idlePersona = JsonCodec.decodeFromString<Session>(
            """{"machine_id":"computer-a","session_id":"main","title":"Persona","assistant":true,"agent":"claude",
                "launcher":"agy","permission_mode":"default","status":"idle","state":"idle"}""",
        )
        val noQueueSessions = object : SessionsRepository {
            override val sessions = MutableStateFlow(listOf(idlePersona, running, blocked))
            override val machines = MutableStateFlow(listOf(Machine("computer-a", "Computer A", online = true)))
            override suspend fun refresh() = Unit
        }
        val tasks = TasksStore(api, connection, scope).also { it.reloadTodos() }
        var style by mutableStateOf(AppStyle.Brief)
        val composer = ChatComposerState().apply {
            focused = true
            value = TextFieldValue("")
        }
        rule.mainClock.autoAdvance = false
        rule.setContent {
            PersonaTheme(darkTheme = true, reduceMotion = true, style = style) {
                Box(
                    Modifier.width(400.dp).height(760.dp)
                        .background(PersonaTheme.palette.background)
                        .testTag("bottom-chrome-shot"),
                ) {
                    key(style) {
                        ChatScreen(
                            main, chats, sender, interactions, noQueueSessions, {},
                            chrome = ChatChromeBindings(
                                actions = actions,
                                connection = connection,
                                tasks = tasks,
                                assistantManagement = management,
                                onOpenSession = {},
                            ),
                            renderStyle = ChatRenderStyle.Assistant,
                            embedded = true,
                            showTopBar = false,
                            composerState = composer,
                        )
                    }
                }
            }
        }
        settle()
        rule.waitUntil(10_000) { rule.onAllNodesWithTag("assistant-tray-bar").fetchSemanticsNodes().isNotEmpty() }

        val minTouchPx = with(rule.density) { Tokens.MinTouch.android.toPx() }
        for (s in AppStyle.entries) {
            style = s
            settle()
            rule.onNode(hasSetTextAction()).performClick()
            settle()
            val trayEntry = rule.onNodeWithTag("assistant-entry-needs").getUnclippedBoundsInRoot()
            val field = rule.onNode(hasSetTextAction()).getUnclippedBoundsInRoot()
            val addNode = rule.onNodeWithContentDescription("Add attachments and quick messages")
            val add = addNode.getUnclippedBoundsInRoot()
            val addTouch = addNode.fetchSemanticsNode().touchBoundsInRoot

            assertTrue("$s no-queue: add button keeps 48dp touch height", addTouch.height >= minTouchPx - 0.5f)
            assertTrue("$s no-queue: input sits above toolbar without overlap", field.bottom <= add.top)

            if (s != AppStyle.Glass) {
                val launcher = rule.onNodeWithText("Default: agy").getUnclippedBoundsInRoot()
                val trayToLauncher = launcher.top - trayEntry.bottom
                // Exact surface gap: AssistantTrays bottom (2dp) + ComposerFrame top (4dp) = 6dp (down from 16dp).
                assertTrue("$s no-queue: tray-to-launcher visible gap ($trayToLauncher) in 4..6.5dp", trayToLauncher in 4.dp..6.5.dp)
                // Launcher chip sits directly above input (no modeHint): gap from chip bottom to field text top stays tight
                // (4dp accessory bottom + input box vertical centering ~12-14dp = <= 20dp, down from ~26dp).
                val launcherToFieldText = field.top - launcher.bottom
                assertTrue("$s no-queue: launcher-to-field gap ($launcherToFieldText) <= 20dp", launcherToFieldText in 4.dp..20.dp)
            } else {
                // Glass has no accessory chip: tray capsule sits 6dp above the composer card, keeping tray-to-field-text <= 26dp
                // (down from ~33dp before removing the capsule's +9dp touch-box padding) while preserving 44dp field / 4dp fieldBottom.
                val trayToFieldText = field.top - trayEntry.bottom
                assertTrue("$s no-queue: tray-to-field gap ($trayToFieldText) in 6..26dp", trayToFieldText in 6.dp..26.dp)
            }
            saveShot("no-queue-open-${s.name.lowercase()}-dark")
        }
    }

    @Test
    fun composerTopHierarchyCoversFoldedExpandedTraysAndOverflowFade() {
        runComposerTopScenario(
            prefix = "top-en",
            styles = AppStyle.entries,
            appearances = listOf(false, true),
            captureExpandedTray = true,
            includeHeldPushes = true,
        )
    }

    @Test
    @Config(sdk = [35], qualifiers = "zh-rCN-w400dp-h760dp-xhdpi")
    fun composerTopHierarchyChineseMatchesPhoneScenario() {
        runComposerTopScenario(
            prefix = "top-zh",
            styles = AppStyle.entries,
            appearances = listOf(false, true),
            captureExpandedTray = false,
            includeHeldPushes = true,
        )
    }

    @Test
    @Config(sdk = [35], qualifiers = "ja-w400dp-h760dp-xhdpi")
    fun composerTopHierarchyJapaneseStaysCompactAndReadable() {
        runComposerTopScenario(
            prefix = "top-ja",
            styles = AppStyle.entries,
            appearances = listOf(true),
            captureExpandedTray = false,
            includeHeldPushes = false,
        )
    }

    private fun runComposerTopScenario(
        prefix: String,
        styles: List<AppStyle>,
        appearances: List<Boolean>,
        captureExpandedTray: Boolean,
        includeHeldPushes: Boolean,
    ) {
        val idlePersonaSession = JsonCodec.decodeFromString<Session>(
            """{"machine_id":"computer-a","session_id":"main","title":"Persona","assistant":true,"agent":"claude",
                "launcher":"agy","permission_mode":"bypassPermissions","effort":"medium","status":"idle","state":"idle"}""",
        )
        val scenarioTodos = listOf(
            Todo("t-run-1", running.title, origin = "main", createdAt = nowIso, updatedAt = nowIso,
                agents = listOf(TodoAgent(running.machineId, running.sessionId, cwd = running.cwd))),
            Todo("t-done-1", "Summarize notes", origin = "main", createdAt = nowIso, updatedAt = nowIso, doneAt = nowIso),
        )
        val heldRow = PushIntercept(
            at = nowIso,
            level = "P1",
            reason = "ln_p1_held",
            title = "Build finished on workstation",
        )
        val scenarioApi = object : TasksApi by api {
            override suspend fun todos() = scenarioTodos
            override suspend fun lnMode() = LnModeResponse(includeHeldPushes)
            override suspend fun pushIntercepts() = PushInterceptsResponse(if (includeHeldPushes) listOf(heldRow) else emptyList())
        }
        val scenarioSessions = object : SessionsRepository {
            override val sessions = MutableStateFlow(listOf(idlePersonaSession, running))
            override val machines = MutableStateFlow(listOf(Machine("computer-a", "Computer A", online = true)))
            override suspend fun refresh() = Unit
        }
        val tasks = TasksStore(scenarioApi, connection, scope).also {
            it.reloadTodos()
        }
        var style by mutableStateOf(styles.first())
        var dark by mutableStateOf(appearances.first())
        var focused by mutableStateOf(false)
        val homeChrome = HomeChrome(
            items = listOf(NavItem("assistant", "Assistant", PhIcons.ChatCircle), NavItem("agents", "Agents", PhIcons.Robot)),
            selected = "assistant",
            onSelect = {},
            leading = {},
        )
        rule.mainClock.autoAdvance = false
        rule.setContent {
            val configuration = Configuration(LocalConfiguration.current).apply {
                uiMode = (uiMode and Configuration.UI_MODE_NIGHT_MASK.inv()) or
                    if (dark) Configuration.UI_MODE_NIGHT_YES else Configuration.UI_MODE_NIGHT_NO
            }
            CompositionLocalProvider(
                LocalConfiguration provides configuration,
                LocalHomeChrome provides homeChrome,
            ) {
                PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                    Box(
                        Modifier.width(400.dp).height(760.dp)
                            .background(PersonaTheme.palette.background)
                            .testTag("bottom-chrome-shot"),
                    ) {
                        key(style, dark, focused) {
                            val composer = ChatComposerState().apply {
                                this.focused = focused
                                value = TextFieldValue("")
                            }
                            ChatScreen(
                                main, chats, sender, interactions, scenarioSessions, {},
                                chrome = ChatChromeBindings(
                                    actions = actions,
                                    connection = connection,
                                    tasks = tasks,
                                    assistantManagement = management,
                                    onOpenSession = {},
                                ),
                                renderStyle = ChatRenderStyle.Assistant,
                                embedded = true,
                                showTopBar = false,
                                composerState = composer,
                            )
                        }
                    }
                }
            }
        }
        settle()
        rule.waitUntil(10_000) { rule.onAllNodesWithTag("assistant-tray-bar").fetchSemanticsNodes().isNotEmpty() }

        for (s in styles) for (isDark in appearances) {
            val suffix = "${s.name.lowercase()}-${if (isDark) "dark" else "light"}"
            // 1. Folded composer (config bar collapsed) with empty Needs you, active In progress, Finished (+ optional Held pushes).
            style = s; dark = isDark; focused = false
            settle()
            val foldedBar = rule.onNodeWithTag("assistant-tray-bar").getUnclippedBoundsInRoot()
            val foldedField = rule.onNode(hasSetTextAction()).getUnclippedBoundsInRoot()
            rule.waitUntil(5_000) {
                if (rule.onAllNodesWithText("Reviewing the latest commit and test results now.").fetchSemanticsNodes().isNotEmpty()) {
                    true
                } else {
                    settle()
                    false
                }
            }
            val foldedReply = rule.onNodeWithText("Reviewing the latest commit and test results now.").getUnclippedBoundsInRoot()
            assertTrue("$prefix folded $suffix: transcript above tray bar", foldedReply.bottom <= foldedBar.top)
            assertTrue("$prefix folded $suffix: tray bar above input field", foldedBar.bottom <= foldedField.top)
            saveShot("$prefix-folded-$suffix")

            // 2. Expanded composer (focused: config metadata strip + quick toolbar shown).
            focused = true
            settle()
            rule.onNode(hasSetTextAction()).performClick()
            settle()
            val openBar = rule.onNodeWithTag("assistant-tray-bar").getUnclippedBoundsInRoot()
            val openField = rule.onNode(hasSetTextAction()).getUnclippedBoundsInRoot()
            val root = rule.onNodeWithTag("bottom-chrome-shot").getUnclippedBoundsInRoot()
            assertTrue("$prefix expanded $suffix: tray bar above input field", openBar.bottom <= openField.top)
            val totalNoQueue = root.bottom - openBar.top
            // Allow graceful 2-line FlowRow wrap on wider CJK fonts (e.g., Linux Noto Sans CJK for Japanese) while staying well below 335dp.
            val maxNoQueue = if (s == AppStyle.Glass) 200.dp else 245.dp
            assertTrue("$prefix expanded $suffix: chrome without queue ($totalNoQueue) <= $maxNoQueue", totalNoQueue <= maxNoQueue)
            saveShot("$prefix-expanded-$suffix")

            // 3. Expanded tray panel to verify active highlight contrast across all 3 styles.
            if (captureExpandedTray && isDark) {
                rule.onNodeWithTag("assistant-entry-running").performClick()
                settle()
                rule.onNodeWithTag("assistant-tray-running").assertIsDisplayed()
                saveShot("$prefix-tray-open-$suffix")
                rule.onNodeWithTag("assistant-entry-running").performClick()
                settle()
            }
        }
    }
}
