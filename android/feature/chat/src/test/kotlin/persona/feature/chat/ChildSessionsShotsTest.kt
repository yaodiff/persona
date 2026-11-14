package persona.feature.chat

import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.*
import androidx.compose.runtime.snapshots.Snapshot
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.flow.flowOf
import org.junit.Assert.*
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.*
import persona.core.contracts.api.*
import persona.core.designsystem.*
import persona.core.domain.lines.ChildSessionCards
import java.io.File

/** Real child cards in every app style, with lazy transcript detail and independent fold state. */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h1500dp-xhdpi")
class ChildSessionsShotsTest {
    @get:Rule val rule = createComposeRule()
    private var style by mutableStateOf(AppStyle.Glass)
    private var dark by mutableStateOf(false)
    private val reads = mutableListOf<SessionKey>()
    private val rows = listOf(
        Session("m", "review", title = "Review the login flow", state = "needs_you", agent = "codex", model = "gpt-5-codex", effort = "high",
            foldedUnder = "p", startedAt = "2026-11-14T10:00:00Z", lastText = "Which sign-in method should I test?"),
        Session("m", "done", title = "Check the migration", state = "idle", agent = "claude", model = "claude-sonnet-4-5", foldedUnder = "p",
            startedAt = "2026-11-14T10:00:00Z", updatedAt = "2026-11-14T10:01:30Z", lastText = "The migration is ready."),
        Session("m", "failed", title = "Run the browser tests", state = "idle", foldedUnder = "p", turnError = "Browser unavailable"),
    )
    private val repository = object : ChatRepository {
        override fun observe(session: SessionKey) = flowOf(ChatSnapshot(items = listOf(
            Item("prompt", role = "user", kind = "text", text = "Check the old and new sign-in flows."),
            Item("tool", role = "assistant", kind = "tool_use", toolName = "Bash"),
            Item("result", role = "assistant", kind = "text", text = "The redirect and retry tests pass."),
        ), loading = false)).also { reads += session }
        override suspend fun loadOlder(session: SessionKey) = Unit
        override suspend fun expandFold(session: SessionKey, fromSeq: Int, toSeq: Int) = emptyList<Item>()
    }

    @Test fun childrenStayCardsAndReadTheirOwnTranscriptOnlyAfterOpening() {
        rule.mainClock.autoAdvance = false
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                val folds = remember { SubagentFolds() }
                CompositionLocalProvider(LocalChatChildren provides ChatChildren(rows.associateBy { it.sessionId }, repository),
                    LocalSubagentFolds provides folds) {
                    Column(Modifier.testTag("shot").width(400.dp).background(PersonaTheme.palette.background).padding(Tokens.Space.l)) {
                        rows.forEach { ChildSessionCard(it.sessionId, assistant = false) }
                    }
                }
            }
        }
        frame()
        assertTrue(reads.isEmpty())
        rule.onAllNodesWithTag("chat.subagent-card", useUnmergedTree = true).assertCountEquals(3)
        rule.onNodeWithText("Running").assertIsDisplayed()
        rule.onNodeWithText("Completed").assertIsDisplayed()
        rule.onNodeWithText("Failed").assertIsDisplayed()
        capture("folded")
        rule.onNodeWithText("Review the login flow").performClick()
        frame()
        rule.onNodeWithText("Check the old and new sign-in flows.").assertIsDisplayed()
        rule.onNodeWithText("The redirect and retry tests pass.").assertIsDisplayed()
        rule.onNodeWithText("Last tool: Bash").assertIsDisplayed()
        assertEquals(listOf(SessionKey("m", "review")), reads)
        capture("opened")
        rule.onNodeWithText("Review the login flow").performClick()
        frame()
        rule.onNodeWithText("Check the old and new sign-in flows.").assertDoesNotExist()
        assertEquals(ChildSessionCards.call("review"), "child:review")
    }

    private fun frame() {
        Snapshot.sendApplyNotifications()
        rule.mainClock.advanceTimeBy(1_000)
        repeat(3) { rule.mainClock.advanceTimeByFrame() }
    }

    private fun capture(scene: String) {
        val out = File(System.getProperty("persona.shots") ?: "build/child-sessions-shots").also { it.mkdirs() }
        for (s in AppStyle.entries) {
            val colors = mutableListOf<Int>()
            for (d in listOf(false, true)) {
                style = s; dark = d; frame()
                val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
                colors += bitmap.getPixel(2, 2)
                out.resolve("children-$scene-${s.name.lowercase()}-${if (d) "dark" else "light"}.png").outputStream().use {
                    bitmap.compress(Bitmap.CompressFormat.PNG, 100, it)
                }
            }
            assertNotEquals("$s dark and light must differ", colors[0], colors[1])
        }
    }
}
