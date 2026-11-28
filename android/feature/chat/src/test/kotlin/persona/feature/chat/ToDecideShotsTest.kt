package persona.feature.chat

import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.assertIsNotEnabled
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithContentDescription
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.onRoot
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performTouchInput
import androidx.compose.ui.test.swipeLeft
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertEquals
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.Interaction
import persona.core.contracts.Question
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import java.io.File

@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h900dp-xhdpi", application = QuestionTestApplication::class)
class ToDecideShotsTest {
    @get:Rule val rule = createComposeRule()

    @Test fun theKeyboardHeightFloorKeepsTheFullCardReachable() {
        val card = ToDecideCard(Interaction("plan", "host", "task-session", kind = "plan", plan = "Read the plan."), true)
        val ids = listOf(card.id, "next")
        val state = ToDecideState(current = card.id)
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                ToDecidePanel(card, ids, 0, state, 48.dp, null, busy = false, uncertain = false,
                    linkVisible = false, blocked = null, onViewSource = null, onAnswer = {})
            }
        }
        rule.onNodeWithText("Open full page").assertIsDisplayed().performClick()
        rule.onNodeWithText("Use Send back to return the plan.").assertIsDisplayed()
    }

    @Test fun pagingSkipAndReturnPreserveTheCardInEachAppearance() {
        val question = Interaction("choose", "host", "assistant", kind = "question", canCancel = true,
            questions = listOf(Question(question = "Which review should I run?")))
        val permission = Interaction("terminal", "host", "task-session", kind = "permission", toolName = "Bash",
            summary = "Run the local formatter")
        val plan = Interaction("plan", "host", "task-session", kind = "plan", plan = "Review the changes before publishing.")
        var cards by mutableStateOf(listOf(ToDecideCard(question, false), ToDecideCard(permission, true, "Example task"),
            ToDecideCard(plan, true, "Example task")))
        val state = ToDecideState(current = question.id)
        var viewSource by mutableStateOf<String?>(null)
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        rule.setContent {
            PersonaTheme(style = style, darkTheme = dark, reduceMotion = true) {
                Column(Modifier.fillMaxWidth().background(PersonaTheme.palette.background)) {
                    val ids = cards.map(ToDecideCard::id)
                    val index = state.selected(ids)
                    val showing = index != null && !state.isCollapsed(ids) && viewSource == null
                    if (showing && index != null) {
                        val card = cards[index]
                        ToDecidePanel(card, ids, index, state, 510.dp, null, busy = false, uncertain = false,
                            linkVisible = false, blocked = null,
                            onViewSource = if (card.task) ({ viewSource = card.id }) else null,
                            onAnswer = { resolution ->
                                if (resolution.cancel == true) {
                                    state.select(ids, index)
                                    cards = cards.filterNot { it.id == card.id }
                                }
                            })
                    }
                    ToDecideEntry(ids, state, showing) { state.show(ids) }
                }
            }
        }
        rule.onNodeWithText("1 / 3").assertIsDisplayed()
        rule.onNodeWithTag("to-decide-panel").performTouchInput { swipeLeft() }
        rule.onNodeWithText("2 / 3").assertIsDisplayed()
        rule.onNodeWithText("Skip").assertIsNotEnabled()
        rule.onNodeWithText("Use Deny to stop the tool.").assertIsDisplayed()
        rule.onNodeWithText("View in Chat").performClick()
        rule.runOnIdle {
            assertEquals("terminal", viewSource)
            viewSource = null
        }
        rule.onNodeWithText("2 / 3").assertIsDisplayed()
        rule.onNodeWithContentDescription("Next card").performClick()
        rule.onNodeWithText("3 / 3").assertIsDisplayed()
        rule.onNodeWithText("Use Send back to return the plan.").assertIsDisplayed()

        val output = File(System.getProperty("persona.shots") ?: "build/to-decide-shots").also { it.mkdirs() }
        for (choice in AppStyle.entries) for (night in listOf(false, true)) {
            rule.runOnIdle { style = choice; dark = night }
            val bitmap = rule.onRoot().captureToImage().asAndroidBitmap()
            val theme = if (night) "dark" else "light"
            File(output, "to-decide-${choice.name.lowercase()}-$theme.png").outputStream()
                .use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
        }
        rule.onNodeWithText("Collapse").performClick()
        rule.onNodeWithTag("to-decide-panel").assertDoesNotExist()
        rule.onNodeWithTag("assistant-entry-to-decide").performClick()
        rule.onNodeWithText("3 / 3").assertIsDisplayed()
        repeat(2) { rule.onNodeWithContentDescription("Previous card").performClick() }
        rule.onNodeWithText("Skip").performClick()
        rule.onNodeWithText("1 / 2").assertIsDisplayed()
        rule.onNodeWithText("Use Deny to stop the tool.").assertIsDisplayed()
    }
}
