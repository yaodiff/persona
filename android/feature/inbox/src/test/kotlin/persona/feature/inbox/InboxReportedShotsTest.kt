package persona.feature.inbox

import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.key
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.SemanticsActions
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.text.TextLayoutResult
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.InboxChainEntry
import persona.core.contracts.InboxEventFields
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import persona.core.domain.inbox.InboxItem
import persona.core.domain.sort.AgentPreferences
import java.io.File
import java.time.Instant
import java.time.ZoneOffset

/** Native-graphics evidence that a long reported original expands inline once, in every style and appearance. */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h1000dp-xhdpi")
class InboxReportedShotsTest {
    @get:Rule val rule = createComposeRule()

    private fun reportedShotsRecord() = InboxRecord(
        item = InboxItem("android-reported-shots", kind = "assistant", title = "Reported detail"),
        source = "assistant",
        event = InboxEventFields(
            chain = listOf(
                InboxChainEntry(
                    id = "android-reported-long-original",
                    at = "2026-01-01T11:59:00Z",
                    kind = "reported",
                    text = "Assistant says: $REPORTED_SHOTS_ORIGINAL",
                    detail = REPORTED_SHOTS_ORIGINAL,
                    via = "assistant",
                ),
            ),
        ),
    )

    private fun reportedShotsLayout(context: String): TextLayoutResult {
        val nodes = rule.onAllNodesWithText(REPORTED_SHOTS_ORIGINAL, useUnmergedTree = true).fetchSemanticsNodes()
        assertEquals("$context: the original must appear exactly once", 1, nodes.size)
        val layouts = mutableListOf<TextLayoutResult>()
        nodes.single().config[SemanticsActions.GetTextLayoutResult].action!!(layouts)
        return layouts.single()
    }

    private fun reportedShotsAssertCollapsed(context: String) {
        val layout = reportedShotsLayout(context)
        assertEquals(
            "$context: the collapsed Text must keep the exact full input",
            REPORTED_SHOTS_ORIGINAL,
            layout.layoutInput.text.text,
        )
        assertTrue(
            "$context: the synthetic original must have at least eight explicit lines",
            REPORTED_SHOTS_ORIGINAL.lines().size >= 8,
        )
        assertEquals("$context: collapsed content must use two lines", 2, layout.lineCount)
    }

    private fun reportedShotsAssertExpanded(context: String) {
        val layout = reportedShotsLayout(context)
        assertEquals(
            "$context: the expanded Text must keep the exact full input",
            REPORTED_SHOTS_ORIGINAL,
            layout.layoutInput.text.text,
        )
        assertTrue(
            "$context: every explicit source line must be laid out",
            layout.lineCount >= REPORTED_SHOTS_ORIGINAL.lines().size,
        )

        val finalGlyph = layout.getBoundingBox(REPORTED_SHOTS_ORIGINAL.lastIndex)
        assertTrue(
            "$context: the final glyph must be nonzero: $finalGlyph",
            finalGlyph.width > 0f && finalGlyph.height > 0f,
        )
        assertTrue(
            "$context: the final glyph must lie inside the original Text: $finalGlyph / ${layout.size}",
            finalGlyph.left >= 0f && finalGlyph.top >= 0f &&
                finalGlyph.right <= layout.size.width && finalGlyph.bottom <= layout.size.height,
        )

        val lastLine = layout.lineCount - 1
        val left = layout.getLineLeft(lastLine)
        val top = layout.getLineTop(lastLine)
        val right = layout.getLineRight(lastLine)
        val bottom = layout.getLineBottom(lastLine)
        assertTrue("$context: the final source line must have nonzero bounds", right > left && bottom > top)
        assertTrue(
            "$context: the final source line must lie inside the original Text: " +
                "[$left, $top, $right, $bottom] / ${layout.size}",
            left >= 0f && top >= 0f && right <= layout.size.width && bottom <= layout.size.height,
        )
    }

    private fun reportedShotsCapture(name: String) {
        rule.waitForIdle()
        val bitmap = rule.onNodeWithTag(REPORTED_SHOTS_TAG).captureToImage().asAndroidBitmap()
        val background = bitmap.getPixel(1, 1)
        var other = 0
        for (y in 0 until bitmap.height step 7) {
            for (x in 0 until bitmap.width step 7) {
                if (bitmap.getPixel(x, y) != background) other++
            }
        }
        assertTrue("$name drew nothing", other > 100)
        val out = File(System.getProperty("persona.shots") ?: "build/inbox-reported-shots").also { it.mkdirs() }
        File(out, "$name.png").outputStream().use {
            assertTrue("$name could not be written", bitmap.compress(Bitmap.CompressFormat.PNG, 100, it))
        }
    }

    @Test
    fun reportedOriginalExpandsAndCollapsesInEveryStyleAndAppearance() {
        val record = reportedShotsRecord()
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var run by mutableIntStateOf(0)
        rule.setContent {
            CompositionLocalProvider(
                LocalInboxClock provides { REPORTED_SHOTS_CLOCK },
                LocalInboxZone provides ZoneOffset.UTC,
            ) {
                PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                    key(run) {
                        LazyColumn(
                            Modifier.testTag(REPORTED_SHOTS_TAG).width(400.dp).height(900.dp)
                                .background(PersonaTheme.palette.background),
                        ) {
                            eventChain(record, emptyList(), AgentPreferences()) { target ->
                                error("The reported original must expand inline, not navigate to $target")
                            }
                        }
                    }
                }
            }
        }

        for (appearance in AppStyle.entries) for (night in listOf(false, true)) {
            val mode = if (night) "dark" else "light"
            val context = "$appearance/$mode"
            rule.runOnIdle {
                style = appearance
                dark = night
                run++
            }
            rule.waitForIdle()
            reportedShotsAssertCollapsed(context)
            rule.onNodeWithText("11:59", useUnmergedTree = true).assertExists()
            rule.onNodeWithText("Assistant says: $REPORTED_SHOTS_ORIGINAL", useUnmergedTree = true).assertDoesNotExist()
            reportedShotsCapture("reported-collapsed-${appearance.name.lowercase()}-$mode")

            rule.onNodeWithText(REPORTED_SHOTS_ORIGINAL).performClick()
            rule.waitForIdle()
            reportedShotsAssertExpanded(context)
            reportedShotsCapture("reported-expanded-${appearance.name.lowercase()}-$mode")

            rule.onNodeWithText(REPORTED_SHOTS_ORIGINAL).performClick()
            rule.waitForIdle()
            reportedShotsAssertCollapsed("$context after collapsing again")
        }
    }

    private companion object {
        const val REPORTED_SHOTS_TAG = "android-reported-shot"
        const val REPORTED_SHOTS_ORIGINAL =
            "The launch review is complete.\n" +
                "Keep the customer announcement.\n" +
                "Keep the owner checklist.\n" +
                "Keep the final rollout notes together.\n" +
                "Every reviewer can verify the same decisions.\n" +
                "Review the customer wording.\n" +
                "Review the owner checklist.\n" +
                "Review the rollout notes before Friday morning.\n" +
                "S1-END"
        val REPORTED_SHOTS_CLOCK: Instant = Instant.parse("2026-01-01T12:00:00Z")
    }
}
