package persona.feature.assistant

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.test.assertCountEquals
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.After
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.Session
import persona.core.contracts.api.AssistantManagerChoice
import persona.core.contracts.api.HubManager
import persona.core.contracts.api.ManagerRoster
import persona.core.designsystem.LocalHomeDrawer
import persona.core.designsystem.LocalManagerRoster
import persona.core.designsystem.PersonaTheme
import java.time.Instant

/**
 * #6401: with several assistants the drawer's head and its main-conversation row read the same name as the switcher —
 * the current manager's `shownName` — not the persona pack's name, so renaming an assistant can no longer show two
 * names. A single installed assistant keeps the product title (AssistantManagersShotsTest pins that design).
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "zh-w400dp-h1200dp-xhdpi")
class AssistantNameTest {
    @get:Rule val rule = createComposeRule()

    private val main = Session(machineId = "hub", sessionId = "main", title = "主对话", status = "idle",
        updatedAt = Instant.now().toString())
    private val multi = ManagerRoster(
        listOf(
            HubManager("assistant", "团子", isDefault = true, builtin = true),
            HubManager("m-work", "工作"),
        ),
        "assistant",
    )

    @After fun cleanup() = AssistantManagerChoice.reset()

    private fun drawer(roster: ManagerRoster?) {
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true) {
                CompositionLocalProvider(LocalManagerRoster provides roster, LocalHomeDrawer provides true) {
                    Box(Modifier.width(380.dp).height(1100.dp).background(PersonaTheme.palette.background)) {
                        AssistantDrawer("", {}, ConversationGroups(main), active = null, hasMain = true,
                            onEnd = null, onMain = {}, onOpen = {}, onInfo = {}, onNew = {}, onSettings = {},
                            onInbox = {}, inboxCount = 0, showTodos = false)
                    }
                }
            }
        }
    }

    @Test fun headAndMainRowReadTheManagersNameWithSeveralAssistants() {
        AssistantManagerChoice.choose("hub", null, "assistant")
        drawer(multi)
        // The compact drawer's head and the main-conversation row, both the manager's name, nowhere the pack's fallback.
        rule.onAllNodesWithText("团子").assertCountEquals(2)
        rule.onAllNodesWithText("Persona").assertCountEquals(0)
    }

    @Test fun aSingleAssistantOrAHubWithoutARosterKeepsThePersonaPacksName() {
        AssistantManagerChoice.choose("hub", null, "assistant")
        var roster: ManagerRoster? by androidx.compose.runtime.mutableStateOf(null)
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true) {
                CompositionLocalProvider(LocalManagerRoster provides roster, LocalHomeDrawer provides true) {
                    Box(Modifier.width(380.dp).height(1100.dp).background(PersonaTheme.palette.background)) {
                        AssistantDrawer("", {}, ConversationGroups(main), active = null, hasMain = true,
                            onEnd = null, onMain = {}, onOpen = {}, onInfo = {}, onNew = {}, onSettings = {},
                            onInbox = {}, inboxCount = 0, showTodos = false)
                    }
                }
            }
        }
        rule.onAllNodesWithText("Persona").assertCountEquals(2)
        roster = ManagerRoster(listOf(HubManager("assistant", "Personal assistant name", isDefault = true)), "assistant")
        rule.waitForIdle()
        rule.onAllNodesWithText("Persona").assertCountEquals(2)
        rule.onAllNodesWithText("Personal assistant name").assertCountEquals(0)
    }
}
