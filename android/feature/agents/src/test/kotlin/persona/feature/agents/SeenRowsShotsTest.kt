package persona.feature.agents

import android.graphics.Bitmap
import androidx.activity.ComponentActivity
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.semantics.SemanticsProperties
import androidx.compose.ui.test.SemanticsMatcher
import androidx.compose.ui.test.assertHasClickAction
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.click
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.onRoot
import androidx.compose.ui.test.performTouchInput
import androidx.compose.ui.test.swipeDown
import androidx.navigation3.runtime.NavKey
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.runBlocking
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.Machine
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.designsystem.AppStyle
import persona.core.designsystem.LocalHomeChrome
import persona.core.designsystem.PersonaTheme
import persona.core.domain.sort.AgentGrouping
import persona.core.navigation.ChatRoute
import persona.core.navigation.Navigator
import java.io.File

@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h1200dp-xhdpi")
class SeenRowsShotsTest {
    @get:Rule val rule = createAndroidComposeRule<ComponentActivity>()

    @Test fun openingFiveRowsKeepsThemVisibleUntilPullToRefresh() {
        val sessions = listOf("Check the release", "Review the notes", "Verify the build", "Update the docs", "Confirm the tests")
            .mapIndexed { index, title ->
                Session("mac", "job-$index", title = title, cwd = "/home/example/project", state = "unread",
                    updatedAt = "2026-01-01T00:00:0${5 - index}Z")
            }
        val dependencies = agentsDependencies(sessions, listOf(Machine("mac", "Example computer", online = true)))
        val preferences = dependencies.preferences()
        val visible = MutableStateFlow<SessionKey?>(null)
        val navigator = object : Navigator by NoNavigation {
            override fun navigate(key: NavKey) {
                if (key is ChatRoute) visible.value = SessionKey(key.machineId, key.sessionId)
            }
            override fun back() { visible.value = null }
        }
        val model = AgentsViewModel(dependencies.sessions(), dependencies.interactions(), dependencies.connection(),
            dependencies.sender(), preferences, visibleChat = visible)
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                CompositionLocalProvider(LocalHomeChrome provides agentsHomeChrome) {
                    AgentsScreen(model, navigator, AgentsMetadata())
                }
            }
        }
        rule.waitUntil(10_000) { model.page.value.loaded }
        rule.runOnIdle {
            model.grouping(AgentGrouping.State)
            model.range(AgentRange.All)
        }
        rule.waitUntil(10_000) { model.page.value.list.groups.any { it.id == "state/unread" && it.rows.size == 5 } }
        for (session in sessions) {
            rule.onNodeWithText(checkNotNull(session.title)).performTouchInput { click() }
            rule.waitUntil(10_000) { visible.value == session.key }
            // This local shell records the same receipt a visible session sends through the shared preferences API.
            runBlocking { preferences.markSeen(listOf(session)) }
            rule.runOnIdle { navigator.back() }
            rule.waitUntil(10_000) {
                model.page.value.list.groups.singleOrNull { it.id == "state/unread" }?.heldSeen?.contains(session.key.path) == true
            }
        }
        val output = File(System.getProperty("persona.shots") ?: "build/seen-rows-shots").also { it.mkdirs() }
        for (appearance in AppStyle.entries) for (night in listOf(false, true)) {
            rule.runOnIdle {
                style = appearance
                dark = night
            }
            rule.onNodeWithText("Clear Seen (5)").assertIsDisplayed().assertHasClickAction()
            sessions.forEach { rule.onNodeWithText(checkNotNull(it.title)).assertIsDisplayed() }
            val variant = if (night) "dark" else "light"
            val bitmap = rule.onRoot().captureToImage().asAndroidBitmap()
            File(output, "seen-rows-${appearance.name.lowercase()}-$variant.png").outputStream()
                .use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
            val status = if (appearance == AppStyle.Board) SemanticsMatcher.expectValue(SemanticsProperties.StateDescription, "Seen")
                else hasText("Seen", substring = true) and !hasText("Clear Seen", substring = true)
            val count = rule.onAllNodes(status, useUnmergedTree = true).fetchSemanticsNodes().size
            assertTrue("${appearance.name} $variant: expected a Seen label on each row, found $count", count >= sessions.size)
        }
        rule.onRoot().performTouchInput { swipeDown(startY = height * 0.2f, endY = height * 0.65f, durationMillis = 500) }
        rule.waitUntil(10_000) { model.page.value.list.groups.none { it.id == "state/unread" } }
        rule.onNodeWithText("Clear Seen (5)").assertDoesNotExist()
    }
}
