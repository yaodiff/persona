package persona.core.designsystem

import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.*
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.testTag
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

@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "zh-w400dp-h400dp-xhdpi")
class AgentFilterCapsuleTest {
    @get:Rule val rule = createComposeRule()

    @Test fun twoUnicodeCharactersAndEllipsis() {
        assertEquals("完成…", collapsedFilterLabel("完成未看"))
        assertEquals("工作…", collapsedFilterLabel("工作中"))
        assertEquals("全部", collapsedFilterLabel("全部"))
        assertEquals("終端", collapsedFilterLabel("終端"))
        assertEquals("😀😃…", collapsedFilterLabel("😀😃😄"))
        assertEquals("Un…", collapsedFilterLabel("Unread"))
    }

    @Test fun selectedLabelExpandsGraduallyWithoutTruncatingCount() {
        var selected by mutableIntStateOf(0)
        rule.mainClock.autoAdvance = false
        rule.setContent {
            PersonaTheme(reduceMotion = false) {
                Box(Modifier.width(400.dp)) {
                    JAgentFilterCapsule(listOf(Bay("全部"), Bay("完成未看", 123)), selected, { selected = it },
                        "分组：按状态", listOf(JAction("按状态", checked = true, onClick = {})))
                }
            }
        }
        rule.mainClock.advanceTimeByFrame()
        val choice = rule.onNodeWithTag("agents-status-1")
        val before = choice.fetchSemanticsNode().boundsInRoot.width
        choice.assertContentDescriptionEquals("完成未看 123")
        rule.runOnIdle { selected = 1 }
        val widths = (0 until 35).map {
            rule.mainClock.advanceTimeByFrame()
            choice.fetchSemanticsNode().boundsInRoot.width
        }
        val after = widths.last()
        assertTrue("width progresses without jumping: $before -> $widths", widths.any { it > before && it < after })
        choice.assertContentDescriptionEquals("完成未看 123")
        choice.assertIsSelected()
    }

    @Test fun allCountKeepsEachStylesExistingVisibility() {
        var style by mutableStateOf(AppStyle.Glass)
        rule.setContent {
            PersonaTheme(style = style, reduceMotion = true) {
                Box(Modifier.width(400.dp)) {
                    JAgentFilterCapsule(listOf(Bay("All", 4), Bay("Working", 1)), 0, {},
                        "Group By: By status", listOf(JAction("By status", checked = true, onClick = {})))
                }
            }
        }
        for (option in AppStyle.entries) {
            rule.runOnIdle { style = option }
            rule.onNodeWithTag("agents-status-0").assertContentDescriptionEquals(if (option == AppStyle.Glass) "All" else "All 4")
            rule.onNodeWithTag("agents-status-0").assertIsSelected()
        }
    }

    @Test fun reduceMotionUsesFinalWidthImmediately() {
        var selected by mutableIntStateOf(0)
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                Box(Modifier.width(400.dp)) {
                    JAgentFilterCapsule(listOf(Bay("全部"), Bay("完成未看", 3)), selected, { selected = it },
                        "分组：按状态", listOf(JAction("按状态", checked = true, onClick = {})))
                }
            }
        }
        val choice = rule.onNodeWithTag("agents-status-1")
        val before = choice.fetchSemanticsNode().boundsInRoot.width
        choice.performClick()
        rule.waitForIdle()
        assertTrue(choice.fetchSemanticsNode().boundsInRoot.width > before)
        choice.assertIsSelected()
        choice.assert(SemanticsMatcher.expectValue(AgentFilterExpanded, true))
    }
}
