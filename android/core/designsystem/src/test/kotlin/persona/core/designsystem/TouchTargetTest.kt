package persona.core.designsystem

import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.size
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.click
import androidx.compose.ui.test.getBoundsInRoot
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithContentDescription
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performTouchInput
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertEquals
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.designsystem.icons.PhIcons

/**
 * Controls drawn smaller than Android's 48dp floor (small and medium buttons, chips, segments, switches, heading actions) still take a
 * tap anywhere in the 48dp around their middle (`docs/design/DESIGN_SYSTEM.md`, section 6.3, DS-06), in every style.
 * One density pixel per dp.
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "zh-w400dp-h900dp-mdpi")
class TouchTargetTest {
    @get:Rule val rule = createComposeRule()

    /** [cell]: the label of the part that takes the tap, when that is not the whole control (one segment). */
    private class Case(val style: AppStyle, val name: String, val cell: String? = null, val control: @Composable (String, () -> Unit) -> Unit) {
        val tag = "$style $name"
    }

    private val floor = 48.dp

    @Test fun smallControlsTakeTapsAcrossTheFloor() {
        val cases = AppStyle.entries.flatMap { style ->
            listOf(
                Case(style, "small button") { tag, tapped -> JButton("确定", tapped, Modifier.testTag(tag), size = JButtonSize.Small) },
                Case(style, "medium button") { tag, tapped -> JButton("确定", tapped, Modifier.testTag(tag), size = JButtonSize.Medium) },
                Case(style, "text button") { tag, tapped -> JTextButton("确定", tapped, Modifier.testTag(tag)) },
                Case(style, "chip") { tag, tapped -> JChip("全部", Modifier.testTag(tag), onClick = tapped) },
                Case(style, "tray entry") { tag, tapped ->
                    AssistantTrayEntry("进行中", 3, AssistantTrayTone.Progress, false, "已折叠", tapped, Modifier.testTag(tag))
                },
                Case(style, "composer meta") { tag, tapped ->
                    ComposerMetaChip("默认: agy", PhIcons.Cube, Modifier.testTag(tag), dropdown = true, onClick = tapped)
                },
                Case(style, "switch") { tag, tapped -> JSwitch(false, { tapped() }, Modifier.testTag(tag)) },
                Case(style, "heading action") { tag, tapped -> HeadingAction(PhIcons.Gauge, "用量", tapped, Modifier.testTag(tag)) },
                Case(style, "toast action", cell = "撤销") { tag, tapped ->
                    JToast(FeedbackNotice("已归档", FeedbackAction("撤销") { tapped() }), dismiss = {}, modifier = Modifier.testTag(tag))
                },
                Case(style, "segment", cell = "Agents") { tag, tapped ->
                    JSegmented(listOf(JSegment("助手"), JSegment("Agents")), 0, { if (it == 1) tapped() }, Modifier.testTag(tag))
                },
                Case(style, "home switcher", cell = "Agents") { tag, tapped ->
                    HomeNavigation(listOf(NavItem("assistant", "助手", PhIcons.ChatCircle), NavItem("agents", "Agents", PhIcons.Robot)),
                        "assistant", { if (it == "agents") tapped() }, leading = {}, modifier = Modifier.testTag(tag))
                },
            )
        }
        var shown by mutableStateOf(cases.first())
        var taps = 0
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true, style = shown.style) {
                Box(Modifier.testTag("stage").size(300.dp), contentAlignment = Alignment.Center) { shown.control(shown.tag) { taps++ } }
            }
        }
        val misses = mutableListOf<String>()
        val reached = mutableListOf<String>()
        for (case in cases) {
            shown = case
            rule.waitForIdle()
            // A clickable merges its label into its own node, so the label finds the segment that takes the tap.
            val bounds = (case.cell?.let { rule.onNodeWithText(it) } ?: rule.onNodeWithTag(case.tag)).getBoundsInRoot()
            val stage = rule.onNodeWithTag("stage").getBoundsInRoot()
            val height = bounds.bottom - bounds.top
            val width = bounds.right - bounds.left
            // Just inside the floor: half of what the drawn size lacks, less a little so rounding stays inside; a
            // side already past the floor is tapped just inside its edge.
            val reach = { size: Dp -> if (size >= floor) (-1).dp else (floor - size) / 2 - 1.dp }
            val x = (bounds.left + bounds.right) / 2
            val y = (bounds.top + bounds.bottom) / 2
            val points = buildList {
                add("above" to (x to bounds.top - reach(height)))
                add("below" to (x to bounds.bottom + reach(height)))
                // The second segment's left side is the first segment's.
                if (case.cell == null) add("left" to (bounds.left - reach(width) to y))
                add("right" to (bounds.right + reach(width) to y))
            }
            fun tap(point: Pair<Dp, Dp>): Boolean {
                val before = taps
                rule.onNodeWithTag("stage").performTouchInput {
                    down(Offset((point.first - stage.left).toPx(), (point.second - stage.top).toPx())); up()
                }
                rule.waitForIdle()
                return taps == before + 1
            }
            for ((side, point) in points) if (!tap(point)) misses += "${case.tag} $side (drawn $width × $height)"
            // Past the floor the control no longer answers: what passes above is the 48dp target, not a stray hit.
            if (height < floor && tap(x to bounds.top - (floor - height) / 2 - 3.dp)) reached += "${case.tag} above"
        }
        assertEquals("taps inside the 48dp floor that missed the control", emptyList<String>(), misses)
        assertEquals("taps past the 48dp floor that still reached the control", emptyList<String>(), reached)
    }

    /**
     * The search field's clear button sits in the field's 48dp row beside the text, so it keeps its 24dp circle (reserving
     * 48dp would make the field taller, #4209) and takes taps above, below and right of it, up to the floor.
     */
    @Test fun theSearchFieldsClearButtonTakesTapsAroundItsCircle() {
        var query by mutableStateOf("文本")
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true, style = AppStyle.Glass) {
                Box(Modifier.size(300.dp), contentAlignment = Alignment.Center) { JSearchField(query, { query = it }, "搜索") }
            }
        }
        val clear = rule.onNodeWithContentDescription("清空搜索", useUnmergedTree = true)
        for (point in listOf(Offset(0.5f, -0.4f), Offset(0.5f, 1.4f), Offset(1.4f, 0.5f))) {
            query = "文本"
            rule.waitForIdle()
            clear.performTouchInput { click(Offset(width * point.x, height * point.y)) }
            rule.waitForIdle()
            assertEquals("a tap at $point of the clear button", "", query)
        }
    }
}
