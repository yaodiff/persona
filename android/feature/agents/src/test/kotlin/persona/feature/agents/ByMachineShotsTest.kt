package persona.feature.agents

import android.graphics.Bitmap
import androidx.activity.ComponentActivity
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.isDialog
import androidx.compose.ui.test.isPopup
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onFirst
import androidx.compose.ui.test.onNodeWithContentDescription
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.onRoot
import androidx.compose.ui.test.performClick
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.lifecycle.viewModelScope
import kotlinx.coroutines.cancel
import kotlinx.coroutines.flow.MutableStateFlow
import org.junit.Assert.assertEquals
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.Machine
import persona.core.contracts.Session
import persona.core.contracts.api.AgentCreationService
import persona.core.contracts.api.AgentDirectories
import persona.core.contracts.api.AgentLauncher
import persona.core.contracts.api.AgentStartRequest
import persona.core.contracts.api.AgentStartResult
import persona.core.designsystem.AppStyle
import persona.core.designsystem.HomeChrome
import persona.core.designsystem.LocalHomeChrome
import persona.core.designsystem.PersonaTheme
import persona.core.domain.sort.AgentGrouping
import java.io.File

/**
 * By Machine (#9319): two computers (one with idle Agents folded), and the grouping menu open, in Glass / Brief / Board,
 * light and dark; en and zh. PNGs in `build/by-machine-shots/` (or `-Dpersona.shots=<dir>`).
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
class ByMachineShotsTest {
    @get:Rule val rule = createAndroidComposeRule<ComponentActivity>()

    private fun at(sec: Long) = "2026-01-01T00:00:${sec.toString().padStart(2, '0')}Z"

    private val sessions = listOf(
        Session("mac", "build", title = "Fix the login bug", cwd = "/Users/me/code/app", state = "working",
            spaceId = "app", spaceName = "app", lastText = "Updating the retry logic.", updatedAt = at(50)),
        Session("mac", "review", title = "Review the release notes", cwd = "/Users/me/code/app", state = "idle",
            spaceId = "app", spaceName = "app", slotName = "review", lastText = "Looks good to merge.", updatedAt = at(40)),
        Session("mac", "research", title = "Compare the cache libraries", cwd = "/Users/me/code/notes", state = "idle",
            spaceId = "notes", spaceName = "notes", slotName = "research", lastText = "Picked a shortlist.", updatedAt = at(30)),
        Session("box", "deploy", title = "Deploy the hotfix", cwd = "/srv/api", state = "needs_you",
            spaceId = "api", spaceName = "api", lastText = "Allow the deploy?", updatedAt = at(20)),
        Session("box", "certs", title = "Rotate the certificates", cwd = "/srv/infra", state = "working",
            spaceId = "infra", spaceName = "infra", lastText = "Waiting on DNS.", updatedAt = at(10)),
    )
    private val machines = listOf(
        Machine("mac", "MacBook Pro", online = true, icon = "laptop"),
        Machine("box", "Build server", online = true, icon = "server"),
        Machine("empty", "Idle rack", online = true),
    )

    private fun out(): File = File(System.getProperty("persona.shots") ?: "build/by-machine-shots").also { it.mkdirs() }

    private fun save(name: String, s: AppStyle, d: Boolean, lang: String = "en") {
        rule.waitForIdle()
        val bitmap = rule.onRoot().captureToImage().asAndroidBitmap()
        File(out(), "$name-$lang-${s.name.lowercase()}-${if (d) "dark" else "light"}.png").outputStream()
            .use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
    }

    @Config(sdk = [35], qualifiers = "en-w400dp-h1200dp-xhdpi")
    @Test fun everyStyleEnglish() = everyStyle("en", "By Machine", "MacBook Pro", "Build server", "2 more idle")

    @Config(sdk = [35], qualifiers = "zh-w400dp-h1200dp-xhdpi")
    @Test fun everyStyleChinese() = everyStyle("zh", "按机器", "MacBook Pro", "Build server", "还有 2 个空闲")

    @Config(sdk = [35], qualifiers = "en-w400dp-h1000dp-xhdpi")
    @Test fun projectHeadingOpensTheExistingNewAgentDialog() {
        val computers = listOf(Machine("mac", "Fixture laptop", online = true, home = "/Users/sample"),
            Machine("box", "Offline server", online = false, home = "/home/sample"))
        val rows = listOf(
            Session("mac", "desktop", title = "Check the desktop site", cwd = "/tmp/site", state = "working", spaceId = "desktop",
                spaceName = "site", workspaceCwd = "/Users/sample/Desktop/site"),
            Session("mac", "code", title = "Check the code site", cwd = "/Users/sample/code/site", state = "working",
                spaceId = "code", spaceName = "site"),
            Session("mac", "loose", title = "Loose agent", cwd = "/Users/sample/tmp", state = "working", spaceName = "No project"),
            Session("box", "unavailable", title = "Offline agent", cwd = "/home/sample/source", state = "working",
                spaceId = "offline", spaceName = "Offline project"),
        )
        val model = agentsModel(rows, computers)
        val service = object : AgentCreationService {
            override val scope = "project-heading-fixture"
            override suspend fun launchers(machineId: String) = listOf(AgentLauncher("codex"))
            override suspend fun directories(machineId: String, path: String) = AgentDirectories("/unrelated")
            override suspend fun start(request: AgentStartRequest, operationId: String) = error("The dialog only opens")
            override suspend fun operation(operationId: String): AgentStartResult = AgentStartResult.Unknown
        }
        val creation = NewAgentViewModel(service, AgentCreationPreferences(rule.activity), MutableStateFlow(computers))
        var dark by mutableStateOf(false)
        try {
            rule.setContent {
                PersonaTheme(darkTheme = dark, reduceMotion = true, style = AppStyle.Glass) {
                    CompositionLocalProvider(LocalHomeChrome provides HomeChrome(emptyList(), "Agents", onSelect = {}, leading = {})) {
                        AgentsScreen(model, NoNavigation, AgentsMetadata(), creationModel = creation)
                    }
                }
            }
            rule.waitUntil(5_000) { model.page.value.loaded }
            rule.runOnIdle { model.grouping(AgentGrouping.MachineSpace) }
            rule.waitUntil(5_000) { model.page.value.list.groups.any { it.projectFolder?.qualifier == "~/Desktop" } }
            val desktop = rule.onNodeWithContentDescription("New Agent in site · ~/Desktop")
            for (appearance in listOf(false, true)) {
                rule.runOnIdle { dark = appearance }
                desktop.assertIsDisplayed()
                rule.onNodeWithContentDescription("New Agent in site · ~/code").assertIsDisplayed()
                rule.onNodeWithContentDescription("New Agent in No project").assertDoesNotExist()
                rule.onNodeWithContentDescription("New Agent in Offline project").assertDoesNotExist()
                save("by-project", AppStyle.Glass, appearance)
            }
            desktop.performClick()
            rule.waitUntil(5_000) { creation.state.value.selection.cwd == "/Users/sample/Desktop/site" }
            assertEquals("mac", creation.state.value.machineId)
            assertEquals("codex", creation.state.value.selection.launcher)
            rule.onNode(isDialog()).assertExists()
        } finally {
            creation.viewModelScope.cancel()
        }
    }

    private fun everyStyle(lang: String, groupingLabel: String, mac: String, box: String, idle: String) {
        val model = agentsModel(sessions, machines)
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                CompositionLocalProvider(LocalHomeChrome provides HomeChrome(emptyList(), "Agents", onSelect = {}, leading = {})) {
                    AgentsScreen(model, NoNavigation, AgentsMetadata())
                }
            }
        }
        rule.waitUntil(5_000) { model.page.value.loaded }
        rule.runOnIdle { model.grouping(AgentGrouping.Machine) }
        for (s in AppStyle.entries) for (d in listOf(false, true)) {
            rule.runOnIdle { style = s; dark = d }
            rule.waitForIdle()
            // Computer names also appear on the filter chips; Needs You cards can repeat a title.
            rule.onAllNodesWithText(mac).onFirst().assertIsDisplayed()
            rule.onAllNodesWithText(box).onFirst().assertIsDisplayed()
            rule.onAllNodesWithText(idle).onFirst().assertIsDisplayed()
            rule.onAllNodesWithText("Fix the login bug").onFirst().assertIsDisplayed()
            rule.onAllNodesWithText("Deploy the hotfix").onFirst().assertIsDisplayed()
            save("by-machine", s, d, lang)
            rule.onNodeWithTag("agents-grouping").performClick()
            rule.onNode(isPopup()).assertIsDisplayed()
            rule.onAllNodesWithText(groupingLabel).onFirst().assertIsDisplayed()
            save("by-machine-menu", s, d, lang)
            // Dismiss the popup before the next style / theme pass.
            rule.onAllNodesWithText(groupingLabel).onFirst().performClick()
            rule.waitForIdle()
        }
    }
}
