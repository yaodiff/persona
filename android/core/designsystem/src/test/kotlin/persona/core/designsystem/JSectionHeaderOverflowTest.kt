package persona.core.designsystem

import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.unit.Density
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode

/** A fitting header is unchanged; an overflowing one moves its actions into a menu in the same layout pass. */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h500dp-xhdpi")
class JSectionHeaderOverflowTest {
    @get:Rule val rule = createComposeRule()

    private fun show(scale: Float) {
        rule.setContent {
            CompositionLocalProvider(LocalDensity provides Density(LocalDensity.current.density, scale)) {
                PersonaTheme(style = AppStyle.Brief, reduceMotion = true) {
                    Box(Modifier.width(400.dp)) {
                        JSectionHeader("Needs you", count = 2, trailing = "Collapse", open = true, onClick = {},
                            overflowActions = listOf(JAction("Mark all as read", onClick = {})), action = {
                                JTextButton("Mark all as read", {})
                            })
                    }
                }
            }
        }
    }

    @Test fun defaultHeaderKeepsTheLegacyRowAndTitle() {
        rule.setContent {
            PersonaTheme(style = AppStyle.Glass, reduceMotion = true) {
                Box(Modifier.width(400.dp)) {
                    JSectionHeader("Needs you", count = 2, trailing = "Collapse", open = true, onClick = {})
                }
            }
        }
        rule.onNodeWithText("Needs you").assertExists()
        rule.onNodeWithText("2").assertExists()
        rule.onNodeWithText("Collapse").assertExists()
        rule.onNodeWithTag("inbox.section.overflow").assertDoesNotExist()
    }

    @Test fun fittingHeaderKeepsItsOrdinaryActions() {
        show(1f)
        rule.onNodeWithText("Needs you").assertExists()
        rule.onNodeWithText("Mark all as read").assertExists()
        rule.onNodeWithTag("inbox.section.overflow").assertDoesNotExist()
    }

    @Test fun overflowingHeaderKeepsTheWholeTitleAndMovesActionsIntoTheMenuWithoutOscillating() {
        var recompositions by mutableIntStateOf(0)
        rule.setContent {
            CompositionLocalProvider(LocalDensity provides Density(LocalDensity.current.density, 2f)) {
                PersonaTheme(style = AppStyle.Brief, reduceMotion = true) {
                    Box(Modifier.width(400.dp)) {
                        JSectionHeader("Needs you", count = 2, trailing = "Collapse", open = true, onClick = {},
                            overflowActions = listOf(JAction("Mark all as read", onClick = {})), action = {
                                JTextButton("Mark all as read", {})
                            })
                        androidx.compose.material3.Text(recompositions.toString())
                    }
                }
            }
        }
        rule.onNodeWithText("Needs you").assertExists()
        rule.onNodeWithText("Mark all as read").assertDoesNotExist()
        rule.onNodeWithTag("inbox.section.overflow").assertExists()
        repeat(2) {
            rule.runOnIdle { recompositions++ }
            rule.waitForIdle()
            rule.onNodeWithText("Needs you").assertExists()
            rule.onNodeWithTag("inbox.section.overflow").assertExists()
            rule.onNodeWithText("Mark all as read").assertDoesNotExist()
        }
        rule.onNodeWithTag("inbox.section.overflow").performClick()
        rule.onNodeWithText("Mark all as read").assertExists()
        rule.onNodeWithText("Collapse").assertExists()
    }
}
