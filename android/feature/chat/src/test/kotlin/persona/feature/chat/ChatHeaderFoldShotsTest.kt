package persona.feature.chat

import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.hasClickAction
import androidx.compose.ui.test.hasContentDescription
import androidx.compose.ui.test.hasScrollToIndexAction
import androidx.compose.ui.test.hasSetTextAction
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.isDialog
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performScrollToIndex
import androidx.compose.ui.test.performTouchInput
import androidx.compose.ui.test.swipeDown
import androidx.compose.ui.test.swipeUp
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.Interaction
import persona.core.contracts.Item
import persona.core.contracts.Machine
import persona.core.contracts.Resolution
import persona.core.contracts.Session
import persona.core.contracts.SessionInfo
import persona.core.contracts.SessionKey
import persona.core.contracts.api.ChatAction
import persona.core.contracts.api.ChatActionResult
import persona.core.contracts.api.ChatActions
import persona.core.contracts.api.ChatRepository
import persona.core.contracts.api.ChatSnapshot
import persona.core.contracts.api.InteractionCenter
import persona.core.contracts.api.MessageSender
import persona.core.contracts.api.OutboxMessage
import persona.core.contracts.api.SessionsRepository
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import java.io.File
import java.time.Instant

/**
 * An Agent's chat header (#4339 C4): open, and after scrolling toward the newest (folded to its slim bar on Glass), in
 * every style, light and dark; and what a tap on the title opens, on Glass (Session Info). Animations off, so the fold
 * comes from the scrolling alone. PNGs in `build/header-fold-shots/` (or `-Dpersona.shots=<dir>`).
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h900dp-xhdpi")
class ChatHeaderFoldShotsTest {
    @get:Rule val rule = createComposeRule()

    private fun out(): File = File(System.getProperty("persona.shots") ?: "build/header-fold-shots").also { it.mkdirs() }

    private val key = SessionKey("macbook", "weather")
    private val start = Instant.parse("2026-11-10T09:00:00Z") // fixed: the times under the bubbles match between runs
    private val said = listOf(
        "Add a home screen widget that shows today's forecast.",
        "I'll add a widget target with a timeline provider, then a small and a medium layout.",
        "Use the same colors as the app's forecast card.",
        "Done: the widget reads the shared forecast cache and uses the card's palette in both sizes.",
        "Does it refresh when the forecast changes?",
        "Yes. The app reloads the widget's timelines after each fetch, and the timeline asks again every 30 minutes.",
    )
    private val items = (1..24).map { n ->
        Item("i$n", n, role = if (n % 2 == 1) "user" else "assistant", kind = "text", text = said[(n - 1) % said.size],
            ts = start.plusSeconds(n * 40L).toString())
    }
    private val base = Session(machineId = key.machineId, sessionId = key.sessionId, title = "Weather widget",
        status = "idle", state = "idle", agent = "claude", paneId = "p1", cwd = "/Users/me/weather",
        spaceName = "weather", spaceId = "w1", slotName = "widget")
    private val sessions = object : SessionsRepository {
        override val machines = MutableStateFlow(listOf(Machine("macbook", "MacBook", online = true)))
        override val sessions = MutableStateFlow(listOf(base,
            base.copy(sessionId = "tests", title = "Widget tests", paneId = "p2", slotName = "tests")))
        override suspend fun refresh() = Unit
    }
    private val transcript = MutableStateFlow(ChatSnapshot(items = items, loading = false))
    private val chats = object : ChatRepository {
        override fun observe(session: SessionKey) = transcript
        override suspend fun loadOlder(session: SessionKey) = Unit
        override suspend fun expandFold(session: SessionKey, fromSeq: Int, toSeq: Int): List<Item> = emptyList()
    }
    private val sender = object : MessageSender {
        override val outbox = MutableStateFlow(emptyList<OutboxMessage>())
        override suspend fun send(session: SessionKey, text: String, clientMessageId: String, clearDraft: String?) = Unit
    }
    private val interactions = object : InteractionCenter {
        override val pending = MutableStateFlow(emptyList<Interaction>())
        override suspend fun refresh() = Unit
        override suspend fun resolve(id: String, resolution: Resolution) = Unit
    }
    private val actions = object : ChatActions {
        override suspend fun isMuted(session: Session) = false
        override suspend fun execute(session: Session, action: ChatAction) = ChatActionResult.Done
        override suspend fun verify(session: Session, action: ChatAction) = ChatActionResult.Done
        override suspend fun info(session: Session) = SessionInfo(sessionId = session.sessionId, model = "claude-opus-4-1",
            launcher = "claude", cwd = session.cwd, gitBranch = "widget", turns = 12, contextTokens = 48_200, outputTokens = 9_100)
    }
    private val renamer = object : persona.core.domain.sort.SessionRenamer {
        override fun project(source: kotlinx.coroutines.flow.Flow<List<Session>>) = source
        override suspend fun rename(session: Session, title: String?, tabName: String?) = true
    }

    /** No idle fold and no animation: the scrolling alone folds it, at once. */
    private fun animationsOff() {
        val context = androidx.test.core.app.ApplicationProvider.getApplicationContext<android.content.Context>()
        android.provider.Settings.Global.putFloat(context.contentResolver, android.provider.Settings.Global.ANIMATOR_DURATION_SCALE, 0f)
    }

    private fun save(bitmap: Bitmap, name: String) =
        File(out(), "$name.png").outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }

    private fun page(style: AppStyle, dark: Boolean) {
        animationsOff()
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                Box(Modifier.fillMaxSize().background(PersonaTheme.palette.background).testTag("page")) {
                    ChatScreen(key, chats, sender, interactions, sessions, onBack = {},
                        chrome = ChatChromeBindings(actions = actions, renamer = renamer, onOpenSession = {}))
                }
            }
        }
        // The lines are built off the main thread: wait for them, not for the spinner.
        rule.waitUntil(20_000) { rule.onAllNodesWithText(said.last(), substring = true).fetchSemanticsNodes().isNotEmpty() }
        rule.waitForIdle()
    }

    /** Open, then scrolled back into the history and toward the newest again. */
    private fun openAndFolded(style: AppStyle, dark: Boolean, suffix: String = "") {
        page(style, dark)
        val name = "${style.name.lowercase()}-${if (dark) "dark" else "light"}$suffix"
        save(rule.onNodeWithTag("page").captureToImage().asAndroidBitmap(), "header-open-$name")
        val list = rule.onNode(hasScrollToIndexAction() and androidx.compose.ui.test.SemanticsMatcher.keyIsDefined(androidx.compose.ui.semantics.SemanticsProperties.VerticalScrollAxisRange))
        list.performTouchInput { swipeDown() }
        rule.waitForIdle()
        list.performTouchInput { swipeUp(endY = top + height * 0.35f) }
        rule.waitForIdle()
        save(rule.onNodeWithTag("page").captureToImage().asAndroidBitmap(), "header-scrolled-$name")
    }

    @Test fun glassLight() = openAndFolded(AppStyle.Glass, dark = false)
    @Test fun glassDark() = openAndFolded(AppStyle.Glass, dark = true)
    @Test fun briefLight() = openAndFolded(AppStyle.Brief, dark = false)
    @Test fun briefDark() = openAndFolded(AppStyle.Brief, dark = true)
    @Test fun boardLight() = openAndFolded(AppStyle.Board, dark = false)
    @Test fun boardDark() = openAndFolded(AppStyle.Board, dark = true)

    /** The largest text: the slim bar's title is cut short, its capsule keeps its middle place. */
    @Config(fontScale = 2f)
    @Test fun glassLargeText() = openAndFolded(AppStyle.Glass, dark = false, suffix = "-large")

    @Config(qualifiers = "en-w320dp-h800dp-xhdpi", fontScale = 2f)
    @Test fun jumpToLatestFloatsAboveComposerWithoutShrinkingTranscriptAtLargeText() {
        page(AppStyle.Glass, dark = true)
        val list = rule.onNode(hasScrollToIndexAction() and
            androidx.compose.ui.test.SemanticsMatcher.keyIsDefined(androidx.compose.ui.semantics.SemanticsProperties.VerticalScrollAxisRange))
        val jump = rule.onNode(hasContentDescription("Jump to latest", substring = true, ignoreCase = true))
        val transcriptBefore = list.fetchSemanticsNode().boundsInRoot
        repeat(2) {
            list.performTouchInput { swipeDown(startY = top + height * 0.25f, endY = bottom - height * 0.15f) }
            rule.waitForIdle()
        }
        val transcript = list.fetchSemanticsNode().boundsInRoot
        val button = jump.fetchSemanticsNode().boundsInRoot
        val composer = rule.onNode(hasSetTextAction()).fetchSemanticsNode().boundsInRoot
        assertEquals("Jump to latest must not shrink the transcript", transcriptBefore, transcript)
        assertTrue("Jump to latest $button overlaps the composer $composer", button.bottom <= composer.top)
        assertTrue("Jump to latest $button sits outside the transcript $transcript",
            button.top >= transcript.top && button.right <= transcript.right)
        save(rule.onNodeWithTag("page").captureToImage().asAndroidBitmap(), "jump-narrow-glass-dark-large")
        jump.performClick()
        rule.waitForIdle()
        jump.assertDoesNotExist()
    }

    @Config(qualifiers = "en-w320dp-h800dp-xhdpi", fontScale = 2f)
    @Test fun collapseControlStaysAboveTheTranscriptAtLargeText() {
        transcript.value = ChatSnapshot(items = buildList {
            add(items.first())
            for (seq in 2..23) {
                add(Item("tool$seq", seq, role = "assistant", kind = "tool_use", toolUseId = "tool$seq",
                    toolName = "Bash", ts = start.plusSeconds(seq.toLong()).toString(),
                    toolInput = buildJsonObject { put("command", "Run widget checks for case $seq") }))
                add(Item("result$seq", seq, role = "user", kind = "tool_result", toolUseId = "tool$seq",
                    text = "Passed", ts = start.plusSeconds(seq.toLong()).toString()))
            }
            add(items.last())
        }, loading = false)
        page(AppStyle.Glass, dark = true)
        val summary = rule.onNode(hasClickAction() and hasContentDescription("Tools 22", substring = true))
        val collapse = rule.onNode(hasClickAction() and hasText("Collapse 22 steps"))
        val list = rule.onNode(hasScrollToIndexAction() and
            androidx.compose.ui.test.SemanticsMatcher.keyIsDefined(androidx.compose.ui.semantics.SemanticsProperties.VerticalScrollAxisRange))
        rule.onNodeWithText("Run widget checks", substring = true).assertDoesNotExist()
        collapse.assertDoesNotExist()
        summary.performClick()
        rule.waitUntil(20_000) {
            rule.onAllNodesWithText("Run widget checks", substring = true).fetchSemanticsNodes().isNotEmpty()
        }
        list.performScrollToIndex(8)
        rule.waitForIdle()
        val button = collapse.fetchSemanticsNode().boundsInRoot
        val reader = list.fetchSemanticsNode().boundsInRoot
        assertTrue("Collapse control $button overlaps the transcript $reader", button.bottom <= reader.top)
        save(rule.onNodeWithTag("page").captureToImage().asAndroidBitmap(), "collapse-narrow-glass-dark-large")
        collapse.performClick()
        rule.waitUntil(20_000) {
            rule.onAllNodes(hasClickAction() and hasContentDescription("Collapsed", substring = true))
                .fetchSemanticsNodes().isNotEmpty()
        }
        summary.assertExists()
        collapse.assertDoesNotExist()
        rule.onNodeWithText("Run widget checks", substring = true).assertDoesNotExist()
    }

    /** A tap on Glass's title: Session Info (main opened the status sheet). A sheet keeps its first frame: a test each. */
    private fun titleTap(dark: Boolean) {
        page(AppStyle.Glass, dark)
        rule.onNode(hasClickAction() and hasText("Weather widget", substring = true)).performClick()
        rule.waitForIdle()
        save(rule.onNode(isDialog()).captureToImage().asAndroidBitmap(), "header-title-tap-glass-${if (dark) "dark" else "light"}")
    }

    @Test fun titleTapLight() = titleTap(dark = false)
    @Test fun titleTapDark() = titleTap(dark = true)
}
