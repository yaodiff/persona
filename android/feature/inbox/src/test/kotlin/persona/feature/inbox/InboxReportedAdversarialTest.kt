package persona.feature.inbox

import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.mutableStateOf
import androidx.compose.ui.Modifier
import androidx.compose.ui.semantics.SemanticsActions
import androidx.compose.ui.test.assertCountEquals
import androidx.compose.ui.test.hasAnyDescendant
import androidx.compose.ui.test.hasClickAction
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.text.TextLayoutResult
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.InboxChainEntry
import persona.core.contracts.InboxChainMessage
import persona.core.contracts.InboxEventFields
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import persona.core.domain.inbox.InboxItem
import persona.core.domain.sort.AgentPreferences
import java.time.Instant
import java.time.ZoneOffset

/** Boundaries that must not be inferred from the long-row happy path alone. */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w360dp-h1000dp-xhdpi")
class InboxReportedAdversarialTest {
    @get:Rule val rule = createComposeRule()

    private val clock = Instant.parse("2026-01-01T12:00:00Z")

    private fun showEntry(entry: () -> InboxChainEntry) {
        rule.setContent {
            val record = InboxRecord(
                InboxItem("reported-adversarial"),
                event = InboxEventFields(chain = listOf(entry())),
            )
            CompositionLocalProvider(
                LocalInboxClock provides { clock },
                LocalInboxZone provides ZoneOffset.UTC,
            ) {
                PersonaTheme(style = AppStyle.Glass, darkTheme = false, reduceMotion = true) {
                    LazyColumn(Modifier.fillMaxSize()) {
                        eventChain(record, emptyList(), AgentPreferences()) { target ->
                            error("A detail-only reported row must not navigate to $target")
                        }
                    }
                }
            }
        }
    }

    private fun entry(
        text: String,
        detail: String?,
        messages: List<InboxChainMessage> = emptyList(),
    ) = InboxChainEntry(
        id = "reported-stable-id",
        at = "2026-01-01T11:02:00Z",
        kind = "reported",
        text = text,
        detail = detail,
        messages = messages,
    )

    private fun row(text: String) = rule.onNode(
        hasClickAction() and hasAnyDescendant(hasText(text)),
        useUnmergedTree = true,
    )

    private fun layout(text: String): TextLayoutResult {
        val nodes = rule.onAllNodesWithText(text, useUnmergedTree = true).fetchSemanticsNodes()
        assertEquals("The original must have exactly one exact-text node", 1, nodes.size)
        val layouts = mutableListOf<TextLayoutResult>()
        nodes.single().config[SemanticsActions.GetTextLayoutResult].action!!(layouts)
        return layouts.single()
    }

    @Test
    fun exactlyTwoFullyVisibleLinesWithTrailingWhitespaceHaveNoExpansionControl() {
        val original = "First visible line\nSecond visible line "
        showEntry { entry("Unused legacy", original) }

        assertEquals("Both source lines already fit, including the trailing space", 2, layout(original).lineCount)
        row(original).assertDoesNotExist()
    }

    @Test
    fun oneFullyVisibleLineWithTrailingWhitespaceHasNoExpansionControl() {
        val original = "Short "
        showEntry { entry("Unused legacy", original) }

        assertEquals("The single source line already fits, including the trailing space", 1, layout(original).lineCount)
        row(original).assertDoesNotExist()
    }

    @Test
    fun reportedMessagesOnlyExpandBelowTheOriginalAndCollapseAgain() {
        val original = "Legacy original"
        val message = "Agent: Supporting message"
        showEntry {
            entry(
                text = original,
                detail = null,
                messages = listOf(InboxChainMessage(sender = "Agent", text = "Supporting message")),
            )
        }

        assertEquals(1, layout(original).lineCount)
        rule.onNodeWithText(message, useUnmergedTree = true).assertDoesNotExist()

        row(original).performClick()
        rule.onNodeWithText(message, useUnmergedTree = true).assertExists()
        rule.onAllNodesWithText(original, useUnmergedTree = true).assertCountEquals(1)

        row(original).performClick()
        rule.onNodeWithText(message, useUnmergedTree = true).assertDoesNotExist()
    }

    @Test
    fun replacingAnExpandedLongOriginalWithShortTextRemovesTheDeadControl() {
        val longOriginal = (1..8).joinToString("\n") { "Original line $it" } + "\nOriginal end"
        val shortOriginal = "Short update"
        val original = mutableStateOf(longOriginal)
        showEntry { entry("Unused legacy", original.value) }

        assertEquals(2, layout(longOriginal).lineCount)
        row(longOriginal).performClick()
        assertTrue("The long original must expand before the update", layout(longOriginal).lineCount >= 9)

        rule.runOnIdle { original.value = shortOriginal }
        rule.waitForIdle()

        assertEquals("The replacement is fully visible on one line", 1, layout(shortOriginal).lineCount)
        row(shortOriginal).assertDoesNotExist()
    }
}
