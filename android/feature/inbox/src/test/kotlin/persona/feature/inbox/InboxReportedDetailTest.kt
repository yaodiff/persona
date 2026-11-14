package persona.feature.inbox

import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.runtime.CompositionLocalProvider
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
import persona.core.contracts.InboxChainLink
import persona.core.contracts.InboxEventFields
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import persona.core.domain.inbox.InboxItem
import persona.core.domain.sort.AgentPreferences
import java.time.Instant
import java.time.ZoneOffset

/** Reported words expand in their original row; semantics text alone does not prove that every line is visible. */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w360dp-h1000dp-xhdpi")
class InboxReportedDetailTest {
    @get:Rule val rule = createComposeRule()

    private val reportedDetailNow = Instant.parse("2026-01-01T12:00:00Z")

    private fun reportedDetailShowChain(entries: List<InboxChainEntry>, go: (ChainTarget) -> Unit = {}) {
        val record = InboxRecord(
            InboxItem("reported-detail-regression"),
            event = InboxEventFields(chain = entries),
        )
        rule.setContent {
            CompositionLocalProvider(
                LocalInboxClock provides { reportedDetailNow },
                LocalInboxZone provides ZoneOffset.UTC,
            ) {
                PersonaTheme(style = AppStyle.Glass, darkTheme = false, reduceMotion = true) {
                    LazyColumn(Modifier.fillMaxSize()) {
                        eventChain(record, emptyList(), AgentPreferences(), go)
                    }
                }
            }
        }
    }

    private fun reportedDetailShowEntry(entry: InboxChainEntry, go: (ChainTarget) -> Unit = {}) =
        reportedDetailShowChain(listOf(entry), go)

    private fun reportedDetailEntry(
        id: String,
        text: String,
        detail: String?,
        kind: String = "reported",
        link: InboxChainLink? = null,
    ) = InboxChainEntry(
        id = id,
        at = "2026-01-01T11:02:00Z",
        kind = kind,
        text = text,
        detail = detail,
        link = link,
    )

    /** Nine explicit lines give the collapsed and expanded checks an oracle independent of overflow flags. */
    private fun reportedDetailLongText(prefix: String, finalMarker: String) =
        (1..8).joinToString("\n") { "$prefix $it" } + "\n$finalMarker"

    private fun reportedDetailRow(text: String) = rule.onNode(
        hasClickAction() and hasAnyDescendant(hasText(text)),
        useUnmergedTree = true,
    )

    private fun reportedDetailLayout(text: String): TextLayoutResult {
        val nodes = rule.onAllNodesWithText(text, useUnmergedTree = true).fetchSemanticsNodes()
        assertEquals("The full original must occur in exactly one exact-text node", 1, nodes.size)
        val layouts = mutableListOf<TextLayoutResult>()
        nodes.single().config[SemanticsActions.GetTextLayoutResult].action!!(layouts)
        return layouts.single()
    }

    private fun reportedDetailAssertFinalGlyphInside(text: String, layout: TextLayoutResult) {
        val finalGlyph = layout.getBoundingBox(text.lastIndex)
        assertTrue(
            "The final glyph must be nonzero: $finalGlyph",
            finalGlyph.width > 0f && finalGlyph.height > 0f,
        )
        assertTrue(
            "The final glyph must lie inside the original Text: $finalGlyph / ${layout.size}",
            finalGlyph.left >= 0f && finalGlyph.top >= 0f &&
                finalGlyph.right <= layout.size.width && finalGlyph.bottom <= layout.size.height,
        )
    }

    private fun reportedDetailAssertLastLineInside(layout: TextLayoutResult) {
        val lastLine = layout.lineCount - 1
        val left = layout.getLineLeft(lastLine)
        val top = layout.getLineTop(lastLine)
        val right = layout.getLineRight(lastLine)
        val bottom = layout.getLineBottom(lastLine)
        assertTrue("The final source line must have nonzero bounds", right > left && bottom > top)
        assertTrue(
            "The final source line must lie inside the original Text: " +
                "[$left, $top, $right, $bottom] / ${layout.size}",
            left >= 0f && top >= 0f && right <= layout.size.width && bottom <= layout.size.height,
        )
    }

    private fun reportedDetailAssertCollapsed(text: String) {
        val layout = reportedDetailLayout(text)
        assertEquals("The collapsed Text must keep the exact full input", text, layout.layoutInput.text.text)
        assertTrue("The synthetic original must have at least eight explicit lines", text.lines().size >= 8)
        assertEquals("Collapsed reported content is progressively disclosed to two lines", 2, layout.lineCount)
    }

    private fun reportedDetailAssertExpanded(text: String) {
        rule.onAllNodesWithText(text, useUnmergedTree = true).assertCountEquals(1)
        val layout = reportedDetailLayout(text)
        assertEquals("The expanded Text must keep the exact full input", text, layout.layoutInput.text.text)
        assertTrue(
            "Every explicit source line must be laid out",
            layout.lineCount >= text.lines().size,
        )
        reportedDetailAssertFinalGlyphInside(text, layout)
        reportedDetailAssertLastLineInside(layout)
    }

    private fun reportedDetailAssertSingleLine(text: String) {
        val layout = reportedDetailLayout(text)
        assertEquals("The Text must keep the exact full input", text, layout.layoutInput.text.text)
        assertEquals("The detail must use exactly one line", 1, layout.lineCount)
        reportedDetailAssertFinalGlyphInside(text, layout)
    }

    @Test
    fun longReportedDetailExpandsOnceInTheOriginalRowAndCollapsesAgain() {
        val detail = reportedDetailLongText("Report line", "R1-END")
        val legacy = "Legacy wording that must not replace the reported detail"
        reportedDetailShowEntry(reportedDetailEntry("reported-long", legacy, detail))

        reportedDetailAssertCollapsed(detail)
        rule.onNodeWithText(legacy, useUnmergedTree = true).assertDoesNotExist()
        rule.onNodeWithText("11:02", useUnmergedTree = true).assertExists()

        reportedDetailRow(detail).performClick()
        reportedDetailAssertExpanded(detail)
        rule.onNodeWithText(legacy, useUnmergedTree = true).assertDoesNotExist()

        reportedDetailRow(detail).performClick()
        reportedDetailAssertCollapsed(detail)
    }

    @Test
    fun blankAndNullReportedDetailsFallBackToTheirLongLegacyText() {
        val blankLegacy = reportedDetailLongText("Blank legacy", "B1-END")
        val nullLegacy = reportedDetailLongText("Null legacy", "N1-END")
        reportedDetailShowChain(listOf(
            reportedDetailEntry("reported-blank", blankLegacy, " \n\t"),
            reportedDetailEntry("reported-null", nullLegacy, null),
        ))

        reportedDetailAssertCollapsed(blankLegacy)
        reportedDetailAssertCollapsed(nullLegacy)
        reportedDetailRow(blankLegacy).performClick()
        reportedDetailAssertExpanded(blankLegacy)
        reportedDetailAssertCollapsed(nullLegacy)

        reportedDetailRow(blankLegacy).performClick()
        reportedDetailAssertCollapsed(blankLegacy)
        reportedDetailRow(nullLegacy).performClick()
        reportedDetailAssertExpanded(nullLegacy)
    }

    @Test
    fun shortReportedDetailHasOneVisibleLineAndNoClickControl() {
        val detail = "Short"
        reportedDetailShowEntry(reportedDetailEntry("reported-short", "Unused legacy", detail))

        reportedDetailAssertSingleLine(detail)
        reportedDetailRow(detail).assertDoesNotExist()
        rule.onNodeWithText("Unused legacy", useUnmergedTree = true).assertDoesNotExist()
    }

    @Test
    fun nonReportedDetailKeepsItsSeparateLowerExpansionAndCollapse() {
        val summary = "Short conclusion"
        val detail = "Separate lower detail"
        reportedDetailShowEntry(reportedDetailEntry("inline-long", summary, detail, kind = "concluded"))

        rule.onNodeWithText(detail, useUnmergedTree = true).assertDoesNotExist()
        reportedDetailRow(summary).performClick()
        reportedDetailAssertSingleLine(detail)

        reportedDetailRow(summary).performClick()
        rule.onNodeWithText(detail, useUnmergedTree = true).assertDoesNotExist()
    }

    @Test
    fun linkedReportedRowNavigatesInsteadOfTogglingInline() {
        val detail = reportedDetailLongText("Linked report", "L1-END")
        val opened = mutableListOf<ChainTarget>()
        reportedDetailShowEntry(
            reportedDetailEntry(
                "reported-linked",
                "Unused legacy",
                detail,
                link = InboxChainLink(type = "item", id = "linked-item"),
            ),
        ) { opened += it }

        reportedDetailAssertCollapsed(detail)
        reportedDetailRow(detail).performClick()
        assertEquals(listOf(ChainTarget.Item("linked-item")), opened)
        reportedDetailAssertCollapsed(detail)
    }
}
