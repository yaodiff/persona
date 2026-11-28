package persona.feature.inbox

import androidx.compose.foundation.layout.Column
import androidx.compose.ui.semantics.SemanticsProperties
import androidx.compose.ui.test.SemanticsMatcher
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performScrollTo
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.designsystem.AppStyle
import persona.core.designsystem.GroupPosition
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.icons.PersonaIcons
import persona.core.domain.inbox.InboxInteraction

@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "en-w400dp-h900dp")
class GlassInboxAlignmentTest {
    @get:Rule val rule = createComposeRule()

    @Test fun interactionIconMatchesSemanticKind() {
        assertEquals(PersonaIcons.taskPlan, inboxInteractionIcon("plan"))
        assertEquals(PersonaIcons.help, inboxInteractionIcon("question"))
        assertEquals(PersonaIcons.terminal, inboxInteractionIcon("screen"))
        assertEquals(PersonaIcons.waitsForYou, inboxInteractionIcon("permission"))
        assertEquals(PersonaIcons.waitsForYou, inboxInteractionIcon(""))
    }

    @Test fun glassInboxAgentRowShowsSessionAskHeadlineAndMachineWithoutPersonaSender() {
        var clicked = false
        val interaction = InboxInteraction(
            id = "ix_plan",
            machineId = "mac",
            sessionId = "s1",
            createdAt = "2026-11-22T05:16:00Z",
            sessionTitle = "Photo sync HEIC",
            summary = "Plan awaiting approval",
            title = "Plan awaiting approval",
            kind = "plan",
            askLine = "Support HEIC",
        )
        rule.setContent {
            PersonaTheme(style = AppStyle.Glass, darkTheme = false, reduceMotion = true) {
                GlassInboxAgentRow(
                    interaction = interaction,
                    machineName = "Mac Studio",
                    position = GroupPosition.Single,
                    dimmed = false,
                    onClick = { clicked = true },
                )
            }
        }
        rule.onNodeWithText("Photo sync HEIC").assertIsDisplayed()
        rule.onNodeWithText("Support HEIC").assertIsDisplayed()
        rule.onNodeWithText("Plan awaiting approval · Mac Studio").assertIsDisplayed()
        assertEquals(0, rule.onAllNodesWithText("Persona").fetchSemanticsNodes().size)
        rule.onNodeWithText("Photo sync HEIC").performClick()
        assertTrue(clicked)
    }

    @Test fun glassFilterStripTogglesSourcesAndKindsWithoutSelectedTabSemantics() {
        var source = ""
        var kind = ""
        var cleared = false
        rule.setContent {
            PersonaTheme(style = AppStyle.Glass, darkTheme = false, reduceMotion = true) {
                Column {
                    GlassInboxFilterStrip(
                        sources = listOf(
                            InboxSourceOption("feishu", "Lark", "lark"),
                            InboxSourceOption("acc1", "me@example.com", "mail"),
                        ),
                        kinds = listOf(
                            "mention" to "@me",
                            "dm" to "DM",
                            "group" to "Group",
                            "mail" to "Mail",
                            "agent" to "Agent",
                        ),
                        activeSource = source,
                        activeKind = kind,
                        onSelectSource = { source = it },
                        onSelectKind = { kind = it },
                        onClearAll = { cleared = true; source = ""; kind = "" },
                    )
                }
            }
        }
        // Source/kind filter pills must not collide with section bar tab Selected semantics.
        assertEquals(0, rule.onAllNodes(SemanticsMatcher.keyIsDefined(SemanticsProperties.Selected)).fetchSemanticsNodes().size)
        rule.onNodeWithText("Lark").performClick()
        assertEquals("feishu", source)
        rule.onNodeWithText("@me").performScrollTo().performClick()
        assertEquals("mention", kind)
        rule.onNodeWithText("All").performScrollTo().performClick()
        assertTrue(cleared)
    }
}
