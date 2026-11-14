package persona.feature.agents

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
import androidx.compose.ui.semantics.SemanticsActions
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.*
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.*
import persona.core.designsystem.*
import persona.core.domain.answer.firstBySession
import persona.core.domain.sort.*
import java.io.File
import java.time.Instant

@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h1000dp-xhdpi")
class FoldedSessionsShotsTest {
    @get:Rule val rule = createComposeRule()
    private var style by mutableStateOf(AppStyle.Glass)
    private var dark by mutableStateOf(false)
    private val parent = Session("m", "p", title = "Refactor the login flow", state = "idle", cwd = "/repo/app",
        children = SessionChildren(3, working = 2, needsYou = 1), runningSubagents = 3, model = "claude-sonnet-4-5")
    private val child = Session("m", "ask", title = "Waiting child", state = "needs_you", foldedUnder = "p")
    private val card = Interaction("q", machineId = "m", sessionId = "ask", sessionTitle = "Review the migration", foldedUnder = "p",
        kind = "question", questions = listOf(Question("Which sign-in method should I test?")))

    @Test fun oneParentRowShowsTheFoldedStateQuestionAndUnduplicatedRunningCount() {
        val list = buildAgentList(listOf(parent, child).map(::AgentSession), settings = AgentListSettings(grouping = AgentGrouping.State))
        assertEquals(1, list.counts[AgentQuickFilter.NeedsYou])
        assertTrue(list.children.isEmpty())
        val row = list.groups.flatMap { it.rows }.single()
        rule.mainClock.autoAdvance = false
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                Column(Modifier.testTag("shot").width(400.dp).background(PersonaTheme.palette.background).padding(Tokens.Space.l)) {
                    SlotRow(row, "Computer", AgentRowMetadata(), Instant.parse("2026-11-14T10:02:00Z"), false, false, false,
                        0, GroupPosition.Single, false, {}, menuItems = null, requestCount = 1,
                        card = listOf(card).firstBySession()[parent.key])
                }
            }
        }
        val out = File(System.getProperty("persona.shots") ?: "build/folded-sessions-shots").also { it.mkdirs() }
        for (s in AppStyle.entries) {
            val colors = mutableListOf<Int>()
            for (d in listOf(false, true)) {
                style = s; dark = d
                Snapshot.sendApplyNotifications()
                rule.mainClock.advanceTimeBy(1_000)
                repeat(3) { rule.mainClock.advanceTimeByFrame() }
                rule.onNodeWithText(parent.title).assertIsDisplayed()
                rule.onNodeWithText(child.title).assertDoesNotExist()
                rule.onNode(hasText("Which sign-in method should I test?", substring = true) and
                    SemanticsMatcher.keyIsDefined(SemanticsActions.GetTextLayoutResult), useUnmergedTree = true).assertIsDisplayed()
                rule.onNodeWithText("Subagents running: 3", substring = true).assertExists()
                val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
                colors += bitmap.getPixel(2, 2)
                out.resolve("parent-${s.name.lowercase()}-${if (d) "dark" else "light"}.png").outputStream().use {
                    bitmap.compress(Bitmap.CompressFormat.PNG, 100, it)
                }
            }
            assertNotEquals(colors[0], colors[1])
        }
    }
}
